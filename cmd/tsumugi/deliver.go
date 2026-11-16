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
// 渡せなかった理由は w に書く。
func deliverPrompt(w io.Writer, d delivery) bool {
	if d.Running != nil && d.Running.Status != "idle" {
		fmt.Fprintf(w, " … 作業中なので次の回に（%s）\n", d.Running.Status)
		return false
	}
	if d.Running != nil {
		if _, err := runClaude("stop", d.Running.ID); err != nil {
			fmt.Fprintln(w, " … 止められず失敗")
			return false
		}
		if !waitStopped(d.Name, 20*time.Second) {
			fmt.Fprintln(w, " … 止まりきらず失敗")
			return false
		}
	}
	if _, err := runClaudeIn(d.Dir, "--bg", "--resume", d.SessionID, d.Prompt); err != nil {
		fmt.Fprintln(w, " … 失敗:", err)
		return false
	}
	fmt.Fprintln(w, " … した")
	return true
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
			if prev.SessionID == "" {
				fmt.Fprintf(w, "%s/%s: 届け %d 件 … まだ一度も起こしていないので次の回に\n", af.ID, name, len(keep))
				continue
			}
			var ps []string
			for _, q := range keep {
				ps = append(ps, "【届け "+q.Received+"】"+q.Prompt)
			}
			d := delivery{Affiliation: af.ID, Name: name, Dir: s.Dir, SessionID: prev.SessionID, Prompt: strings.Join(ps, "\n\n")}
			if a, ok := newestRunning(all, name); ok {
				d.Running = &a
			}
			fmt.Fprintf(w, "%s/%s: 届け %d 件を渡す", af.ID, name, len(keep))
			if dry {
				fmt.Fprintf(w, "（しない）%q\n", d.Prompt)
				continue
			}
			if !deliverPrompt(w, d) {
				continue
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
	all, err := listAllAgents()
	if err != nil {
		return err
	}
	_, err = deliverQueue(os.Stdout, []affiliation{af}, states, all, time.Now(), false)
	return err
}
