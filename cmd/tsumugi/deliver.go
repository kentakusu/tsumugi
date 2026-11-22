package main

// 届ける: 外（受け口・人・ほかの道具）から、紡のセッションへ指示を渡す。
//
//   tsumugi 届ける <名前> [指示…]     指示が無ければ標準入力を 1 件として
//
// 渡し方は定時と同じ（止まっていれば同じ会話のまま指示を付けて起こす・空いていれば止めてから・
// 作業中なら待つ）。すぐ渡せないものは <所属>/届け待ち/ に置き、5 分おきの run が渡す。
// 受け口（Reader の listen など）は Claude から切り離して常駐させ、来たものをこれで渡す。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const queueDir = "届け待ち"

// 渡せないまま、これより古くなったものは諦める（見張りの知らせ先へ知らせる）。
const queueGiveUp = 24 * time.Hour

type queued struct {
	Name     string `json:"名前"`
	Prompt   string `json:"指示"`
	Received string `json:"受けた"`
	file     string
}

func queuePath(a affiliation) string { return filepath.Join(filepath.Dir(a.Path), queueDir) }

// 名前から所属とセッションを引く。
func findSession(affs []affiliation, name string) (affiliation, sessionSpec, bool) {
	for _, af := range affs {
		for _, s := range af.Sessions {
			if s.Name == name {
				return af, s, true
			}
		}
	}
	return affiliation{}, sessionSpec{}, false
}

func enqueue(af affiliation, name, prompt string, now time.Time) (string, error) {
	dir := queuePath(af)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var r [4]byte
	_, _ = rand.Read(r[:])
	q := queued{Name: name, Prompt: prompt, Received: now.Format(time.RFC3339)}
	b, _ := json.MarshalIndent(q, "", "  ")
	p := filepath.Join(dir, strconv.FormatInt(now.UnixNano(), 10)+"-"+hex.EncodeToString(r[:])+".json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return "", err
	}
	return p, os.Rename(tmp, p)
}

// 届け待ちを古い順に読む。
func loadQueue(af affiliation) ([]queued, error) {
	dir := queuePath(af)
	es, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []queued
	for _, e := range es {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var q queued
		if err := json.Unmarshal(b, &q); err != nil {
			return nil, fmt.Errorf("%s を読めません: %w", p, err)
		}
		q.file = p
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].file < out[j].file })
	return out, nil
}

// 指示を 1 つのセッションへ渡す（定時と届けで共通）。渡せたら true。
// Claude Code が写し（新しい会話 ID）を作ったときは、その ID を返す（呼ぶ側が控えを書き換える）。
// 渡せなかった理由は w に書く。
func deliverPrompt(w io.Writer, d delivery) (bool, string) {
	// チャネルで渡せれば、止めずに渡す（作業中でも、セッションの中で次のターンまで待ってくれる）。
	if d.Running != nil {
		if root, err := homeDir(); err == nil {
			if ok, err := channelDeliver(root, d.Affiliation, d.Name, d.Prompt); ok {
				fmt.Fprintln(w, " … した（チャネル・止めずに）")
				return true, ""
			} else if err != nil {
				fmt.Fprintln(w, " … チャネルが応えないので、止めて渡す:", err)
			}
		}
	}
	if d.Running == nil {
		// 止まっているものを --resume で起こす道。daemon が立ち上がったばかりだと空の会話になる（jobrecord.go）。
		if left := daemonJustStarted(time.Now()); left > 0 {
			fmt.Fprintf(w, " … Claude Code が立ち上がったばかりなので次の回に（あと %d 秒は待つ）\n", int(left.Seconds())+1)
			return false, ""
		}
	}
	if d.Running != nil && d.Running.Status != "idle" {
		fmt.Fprintf(w, " … 作業中なので次の回に（%s）\n", d.Running.Status)
		return false, ""
	}
	if d.spec.Name != "" && !jobKeepsSettings(d.SessionID, d.Name) {
		// ★Claude Code が起こした時の設定を覚えていない（jobrecord.go）。フラグ無しで渡すと、名前もチャネルも
		// 権限モードも抜けたまま起きるか、写しになる（2026-11-22 Reatex・UseAI で実測）。
		// 同じ会話 ID のまま紡の設定で付け直し、指示はそのとき渡す（refitSameID。写しを作らない）。
		fmt.Fprint(w, "（Claude Code が設定を覚えていないので、同じ会話のまま紡の設定を付け直す）")
		if !refitSameID(w, d.af, d.spec, d.SessionID, d.Prompt, d.Running) {
			return false, ""
		}
		fmt.Fprintln(w, " … した")
		return true, ""
	}
	// ★設定を覚えている会話は、claude respawn で同じ ID・同じ設定のまま起こし（動いていれば起こし直し）、チャネルで渡す。
	// --resume <ID> "指示" は、Claude Code が予備のプロセス（spare）を使うと新しい ID の写しになる
	// （2026-11-22 UseAI・Reatex: daemon.log の「claimed-spare <新しい ID> (shell)」。claude respawn は
	// 「claimed-spare <同じ ID> (fleet)」で同じ ID のまま起きた）。チャネルが使えない所だけ、今までの --resume で渡す。
	if channelOn() {
		if root, err := homeDir(); err == nil {
			return respawnAndDeliver(w, root, d), ""
		}
	}
	if d.Running != nil {
		if _, err := runClaude("stop", d.Running.ID); err != nil {
			fmt.Fprintln(w, " … 止められず失敗")
			return false, ""
		}
		if !waitStopped(d.Name, 20*time.Second) {
			fmt.Fprintln(w, " … 止まりきらず失敗")
			return false, ""
		}
		time.Sleep(stopSettle) // 止まった直後に --resume すると写しになることがある
	}
	t0 := time.Now()
	// 設定を覚えている会話: フラグ無しの --resume ＋指示で、同じ ID・保存した設定のまま起きる
	// （claude の note「woke session … with its saved options」・2026-11-22 Windows で実測）。
	args := []string{"--bg", "--resume", d.SessionID, d.Prompt}
	if _, err := runClaudeIn(d.Dir, args...); err != nil {
		fmt.Fprintln(w, " … 失敗:", err)
		return false, ""
	}
	resumed[d.Name] = true
	newID := adoptCopy(d, t0)
	if newID != "" {
		fmt.Fprintf(w, " … した（★写しになった: %s → %s。紡はこちらを追う）\n", short(d.SessionID), short(newID))
		if all, err := listAllAgents(); err == nil {
			retireOldID(w, d.SessionID, all)
		}
		return true, newID
	}
	fmt.Fprintln(w, " … した")
	return true, ""
}

// claude respawn で同じ会話 ID のまま起こし、チャネルが応えるのを待って指示を渡す。渡せたら true。
// 渡せなかったら false（届け・定時は残り、次の回にまた渡す）。
func respawnAndDeliver(w io.Writer, root string, d delivery) bool {
	if _, err := runClaude("respawn", short(d.SessionID)); err != nil {
		fmt.Fprintln(w, " … claude respawn が失敗:", err)
		return false
	}
	resumed[d.Name] = true
	if _, ok := waitRunningID(d.Name, d.SessionID, respawnWait); !ok {
		fmt.Fprintln(w, " … claude respawn したが、まだ一覧に出ないので次の回に")
		return false
	}
	var last error
	for end := time.Now().Add(respawnWait); ; time.Sleep(time.Second) {
		ok, err := channelDeliver(root, d.Affiliation, d.Name, d.Prompt)
		if ok {
			fmt.Fprintln(w, " … した（claude respawn で同じ会話のまま起こし、チャネルで渡した）")
			return true
		}
		last = err
		if !time.Now().Before(end) {
			break
		}
	}
	fmt.Fprintln(w, " … claude respawn で起こしたが、チャネルが応えないので次の回に:", last)
	return false
}

// claude respawn のあと、一覧に出てチャネルが応えるまで待つ長さ。テストで縮める。
var respawnWait = 45 * time.Second

// 常駐しないセッションを、届け・定時のために初めて起こす。最初の指示と渡す指示の両方を付けて渡す。
// 起きた会話の ID を返す（一覧にまだ出なければ ""。名前で run が拾う）。
func startFresh(w io.Writer, af affiliation, s sessionSpec, prompt string) (string, bool) {
	fmt.Fprint(w, " … まだ一度も起こしていないので、最初の指示と一緒に起こす")
	if s.Trust {
		if err := trustBeforeStart(io.Discard, action{Affiliation: af.ID, Name: s.Name, Dir: s.Dir}, false); err != nil {
			fmt.Fprintln(w, " … 作業フォルダを信頼済みにできず失敗:", err)
			return "", false
		}
	}
	spec := s
	spec.Prompt = prompt
	if s.Prompt != "" {
		spec.Prompt = s.Prompt + "\n\n" + prompt
	}
	if _, err := runClaudeIn(s.Dir, startArgs(af, spec)...); err != nil {
		fmt.Fprintln(w, " … 失敗:", err)
		return "", false
	}
	resumed[s.Name] = true
	startedNow[s.Name] = true
	a, ok := waitRunning(s.Name, resumeWait)
	if !ok {
		fmt.Fprintln(w, " … した（まだ一覧に出ない）")
		return "", true
	}
	fmt.Fprintln(w, " … した")
	return a.SessionID, true
}

var stopSettle = 3 * time.Second // テストで縮める

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// 起こした会話が出るのを待つ。同じ ID で出れば ""。出ずに、同じ作業フォルダで t0 以降に起きた
// 別の会話があれば、それを写しとして引き取る（ID を返す）。
func adoptCopy(d delivery, t0 time.Time) string {
	dir := expandHome(d.Dir)
	for end := time.Now().Add(resumeWait); ; time.Sleep(time.Second) {
		all, err := listAllAgents()
		if err == nil {
			var cand agent
			for _, a := range all {
				if a.Pid == 0 {
					continue
				}
				if a.SessionID == d.SessionID {
					return ""
				}
				if a.Cwd == dir && time.UnixMilli(a.StartedAt).After(t0.Add(-2*time.Second)) && a.StartedAt > cand.StartedAt {
					cand = a
				}
			}
			// ★中身の無い会話は引き取らない（hasTranscript）。写しの記録が書かれるまで待つ時間の内で待ち、
			// 出なければ引き取らずに次の run の record に任せる（record も中身を確かめてから乗り換える）。
			if cand.SessionID != "" && hasTranscript(cand.SessionID) {
				knownIDs[d.Name] = cand.SessionID
				return cand.SessionID
			}
		}
		if !time.Now().Before(end) {
			return ""
		}
	}
}

// 届け待ちを渡す。セッションごとにまとめて 1 回で渡す。
func deliverQueue(w io.Writer, affs []affiliation, states map[string]affState, all []agent, now time.Time, dry bool) (bool, error) {
	machine := thisMachine()
	any := false
	for _, af := range affs {
		qs, err := loadQueue(af)
		if err != nil {
			return any, err
		}
		byName := map[string][]queued{}
		var names []string
		for _, q := range qs {
			if _, ok := byName[q.Name]; !ok {
				names = append(names, q.Name)
			}
			byName[q.Name] = append(byName[q.Name], q)
		}
		for _, name := range names {
			list := byName[name]
			_, s, ok := findSession([]affiliation{af}, name)
			if ok && !s.here(machine) {
				continue // よそのマシンの紡が渡す
			}
			// 古すぎるものは諦める
			var keep []queued
			for _, q := range list {
				t, err := time.Parse(time.RFC3339, q.Received)
				if (err == nil && now.Sub(t) > queueGiveUp || !ok) && len(af.Watch.Notify) == 0 {
					// ★知らせ先が無いのに諦めると、誰も読まない 諦めた/ に移るだけで実質 失われる
					// （2026-11-21 yui-windows の実測）。知らせ先が無いうちは諦めずに置いておく。
					fmt.Fprintf(w, "★%s/%s への届けが %s から渡せていません（知らせ先が無いので、諦めずに置いておきます）\n", af.ID, name, q.Received)
					keep = append(keep, q)
					continue
				}
				if err == nil && now.Sub(t) > queueGiveUp || !ok {
					why := "渡せないまま 24 時間たった"
					if !ok {
						why = "所属.json にそのセッションが無い"
					}
					msg := fmt.Sprintf("【紡】%s/%s への届けを諦めました（%s）。受けた %s。中身は %s", af.ID, name, why, q.Received, giveUpPath(q.file))
					fmt.Fprintln(w, msg)
					if !dry {
						if err := giveUp(q.file); err != nil {
							return any, err
						}
						notify(w, af, msg)
					}
					continue
				}
				keep = append(keep, q)
			}
			if len(keep) == 0 {
				continue
			}
			prev := states[af.ID].Sessions[name]
			if prev.Held {
				fmt.Fprintf(w, "%s/%s: 届け %d 件 … 止めておく印があるので溜めておく（tsumugi a で起こすと渡る）\n", af.ID, name, len(keep))
				continue
			}
			if prev.SessionID == "" && s.resident() {
				// 常駐するものは、run が最初の指示を付けて起こしてから渡す
				fmt.Fprintf(w, "%s/%s: 届け %d 件 … まだ一度も起こしていないので次の回に\n", af.ID, name, len(keep))
				continue
			}
			var ps []string
			for _, q := range keep {
				ps = append(ps, "【届け "+q.Received+"】"+q.Prompt)
			}
			d := delivery{Affiliation: af.ID, Name: name, Dir: s.Dir, SessionID: prev.SessionID, Prompt: strings.Join(ps, "\n\n"), af: af, spec: s}
			if a, ok := newestRunning(all, name); ok {
				d.Running = &a
			}
			fmt.Fprintf(w, "%s/%s: 届け %d 件を渡す", af.ID, name, len(keep))
			if dry {
				if d.SessionID == "" {
					fmt.Fprint(w, "（常駐しない・まだ一度も起こしていないので、最初の指示と一緒に起こす）")
				}
				fmt.Fprintf(w, "（しない）%q\n", d.Prompt)
				continue
			}
			var done bool
			var newID string
			if d.SessionID == "" {
				newID, done = startFresh(w, af, s, d.Prompt)
				if done {
					st := states[af.ID]
					st.Sessions[name] = sessionState{SessionID: newID} // ID が見えなくても、控えに載せて run の record に拾わせる
					if err := saveState(af, st); err != nil {
						return true, err
					}
					newID = ""
				}
			} else {
				done, newID = deliverPrompt(w, d)
			}
			if !done {
				continue
			}
			if newID != "" {
				st := states[af.ID]
				ss := st.Sessions[name]
				ss.moveTo(newID, "写しになった")
				st.Sessions[name] = ss
				if err := saveState(af, st); err != nil {
					return true, err
				}
			}
			for _, q := range keep {
				if err := os.Remove(q.file); err != nil {
					return true, err
				}
			}
			any = true
		}
	}
	return any, nil
}

// この run で起こし直したセッション。起こした直後は claude agents にまだ出ないことがあり、
// そこで「止まっている」と読んで --resume すると、動いている会話の写しができる（2026-11-17 実測）。
// 同じ run の中では、二度 起こさない。
var resumed = map[string]bool{}

var resumeWait = 15 * time.Second // テストで縮める

func justResumed(name string) {
	resumed[name] = true
	waitRunning(name, resumeWait)
}

func giveUpPath(p string) string {
	return filepath.Join(filepath.Dir(p), "諦めた", filepath.Base(p))
}

func giveUp(p string) error {
	if err := os.MkdirAll(filepath.Dir(giveUpPath(p)), 0o700); err != nil {
		return err
	}
	return os.Rename(p, giveUpPath(p))
}

func cmdDeliver(args []string, stdin io.Reader) error {
	if len(args) < 1 {
		return fmt.Errorf("使い方: tsumugi 届ける <名前> [指示…]")
	}
	name := args[0]
	prompt := strings.Join(args[1:], " ")
	if prompt == "" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		prompt = strings.TrimRight(string(b), "\n")
	}
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("指示が空です")
	}
	root, err := homeDir()
	if err != nil {
		return err
	}
	affs, err := loadAffiliations(root)
	if err != nil {
		return err
	}
	af, _, ok := findSession(affs, name)
	if !ok {
		return fmt.Errorf("%q というセッションは、どの所属.json にもありません", name)
	}
	p, err := enqueue(af, name, prompt, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("届け待ちに置きました: %s\n", p)
	// run の中（見張りの「知らせる」から呼ばれたとき）は、錠を run が持っているので、置くだけにする
	// （次の run が渡す）。待つと run が止まる。
	if os.Getenv("TSUMUGI_IN_RUN") != "" {
		return nil
	}
	// すぐ渡してみる。錠が取れなければ、5 分おきの run に任せる。
	unlock, err := lockHome(root, 60*time.Second)
	if err != nil {
		fmt.Println("ほかの run が動いているので、次の回に渡します")
		return nil
	}
	defer unlock()
	states := map[string]affState{}
	st, err := loadState(af)
	if err != nil {
		return err
	}
	states[af.ID] = st
	learnIDs(states)
	all, err := listAllAgents()
	if err != nil {
		return err
	}
	_, err = deliverQueue(os.Stdout, []affiliation{af}, states, all, time.Now(), false)
	return err
}
