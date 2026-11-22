package main

// Claude Code の背景セッションの記録（<Claude の設定の置き場>/jobs/<会話 ID の先頭 8 字>/state.json）を読む。
//
// 紡は「起こし直す」で、フラグ無しの claude --bg --resume <ID> を打つ（フラグを付けると写しになるため）。
// フラグ無しで起きたセッションが紡の設定（名前・チャネル・権限モード・--settings の env・結の決まり）を
// 持っているのは、Claude Code がこの記録にそれを覚えているときだけ。
// ★WSL を起こし直したあと、この記録が無いまま --resume すると、名前の無い新しい記録で起き、
//   Claude Code が中身から名前を自動で付け（nameSource: auto）、respawnFlags は空になる
//   （2026-11-22 UseAI・Reatex で実測: useai-main が cc-34-migration-handoff、reatex-main が lw-monitoring-patrol に）。
// ★記録が無い会話に紡のフラグを全部付けて --resume すると、写しにならず同じ ID のまま、名前も保たれて起きる
//   （2026-11-22 Windows・2.1.330 で実測: nameSource: user・respawnFlags に -n が入った）。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 起こし直しで紡の設定を付け直すときに渡す知らせ（指示が無いと空の会話で起きるので、必ず付ける）。
const refitNotice = "【紡】Claude Code が起こした時の設定を覚えていなかったので、紡の設定（名前・チャネル・権限モードなど）を付け直して起こし直しました。" +
	"会話はそのまま続いています。続きの作業があれば続け、無ければ待機してください。"

type jobRecord struct {
	Name         string   `json:"name"`
	NameSource   string   `json:"nameSource"`
	State        string   `json:"state"`
	RespawnFlags []string `json:"respawnFlags"`
}

// Claude Code の設定の置き場（~/.claude。CLAUDE_CONFIG_DIR があればそれ）。
func claudeHome() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".claude"), nil
}

// 会話 ID の job の記録。無い・読めないなら ok=false。テストで差し替える。
var readJobRecord = func(sessionID string) (jobRecord, bool) {
	if len(sessionID) < 8 {
		return jobRecord{}, false
	}
	home, err := claudeHome()
	if err != nil {
		return jobRecord{}, false
	}
	b, err := os.ReadFile(filepath.Join(home, "jobs", sessionID[:8], "state.json"))
	if err != nil {
		return jobRecord{}, false
	}
	var j jobRecord
	if json.Unmarshal(b, &j) != nil {
		return jobRecord{}, false
	}
	return j, true
}

// 紡のセッションを、同じ会話 ID のまま紡の設定（名前・チャネル・権限モード・env・結の決まり）で起こし直す。
// 写し（新しい ID）を作らないので、claude agents と紡の控えが同じ ID・同じ名前のまま揃う。
//
//	動いていれば止める（空いているときだけ） → claude rm（Claude Code 側の記録だけ外す。会話は残る）
//	→ 紡のフラグを付けて claude --bg --resume <ID> "<指示>"
//
// ★記録が残っている会話にフラグを付けて --resume すると写しになる。記録を外してからだと、同じ ID のまま
//
//	フラグどおりに起きる（2026-11-22 Windows・2.1.330 で実測: e415b202 が名前・フラグ付きで同じ ID、前の会話も持って起きた）。
//
// ★指示を必ず付ける。指示の無いフラグ付きの --resume は、会話を読み込まない空の会話で起きる（useai-main の実測）。
// ★claude rm は、worktree に未 push の変更があると断る。そのときは付け直さずに知らせる（変更を消さない）。
// 渡せたら true。
func refitSameID(w io.Writer, af affiliation, s sessionSpec, sessionID, prompt string, running *agent) bool {
	if running != nil {
		if running.Status != "idle" {
			fmt.Fprintf(w, " … 作業中なので次の回に（%s）\n", running.Status)
			return false
		}
		if _, err := runClaude("stop", running.ID); err != nil {
			fmt.Fprintln(w, " … 止められず失敗:", err)
			return false
		}
		if !waitStoppedID(sessionID, 20*time.Second) {
			fmt.Fprintln(w, " … 止まりきらず失敗")
			return false
		}
		time.Sleep(stopSettle)
	}
	if _, err := runClaude("rm", short(sessionID)); err != nil && !strings.Contains(err.Error(), "No session matching") {
		fmt.Fprintf(w, " … ★claude rm %s が断ったので付け直せません（worktree に未 push の変更があるなど）: %v\n", short(sessionID), err)
		return false
	}
	if s.Trust {
		if err := trustBeforeStart(io.Discard, action{Affiliation: af.ID, Name: s.Name, Dir: s.Dir}, false); err != nil {
			fmt.Fprintln(w, " … 作業フォルダを信頼済みにできず失敗:", err)
			return false
		}
	}
	spec := s
	spec.Prompt, spec.From = prompt, sessionID
	if _, err := runClaudeIn(s.Dir, startArgs(af, spec)...); err != nil {
		fmt.Fprintln(w, " … 失敗:", err)
		return false
	}
	resumed[s.Name] = true
	startedNow[s.Name] = true
	return true
}

// 会話 ID で動いているものが止まるのを待つ。
func waitStoppedID(sessionID string, d time.Duration) bool {
	for end := time.Now().Add(d); ; time.Sleep(time.Second) {
		all, err := listAllAgents()
		if err == nil {
			alive := false
			for _, a := range all {
				if a.SessionID == sessionID && a.Pid > 0 {
					alive = true
				}
			}
			if !alive {
				return true
			}
		}
		if !time.Now().Before(end) {
			return false
		}
	}
}

// 紡のセッションを配下に揃える（動いているもの）。
//   - 紡が控えた会話が動いているのに、Claude Code 側に紡の設定が無い（名前が自動で付け替わった・failed から起きた等）
//   - 所属.json の名前で、紡が控えていない会話が動いている（人が claude で手で起こした等）
//
// どちらも、空いていれば同じ会話 ID のまま紡の設定で付け直し（refitSameID）、控えに入れる。作業中なら次の回に回す。
// 紡の設定が既に揃っている手で起こしたものは、止めずにそのまま控える。
func adoptRunning(w io.Writer, machine string, affs []affiliation, states map[string]affState, all []agent, dry bool) bool {
	any := false
	for _, af := range affs {
		st := states[af.ID]
		for _, sp := range af.Sessions {
			if !sp.here(machine) {
				continue
			}
			ss, known := st.Sessions[sp.Name]
			var a agent
			found := false
			if known && ss.SessionID != "" {
				for _, x := range all {
					if x.Pid > 0 && x.SessionID == ss.SessionID {
						a, found = x, true
					}
				}
			}
			adopting := false
			if !found {
				// 控えの会話が動いていない。同じ名前で動いているもの（手で起こしたもの）があれば配下に入れる
				var best agent
				for _, x := range all {
					// 画面で開いているもの（interactive）は、人が打ち込んでいるので止めない（一覧には出す）
					if x.Pid > 0 && x.Kind != "interactive" && x.Name == sp.Name && x.StartedAt > best.StartedAt {
						best = x
					}
				}
				if best.SessionID == "" {
					continue
				}
				a, adopting = best, true
				// ★中身の無い会話は配下に入れない。Claude Code が会話を読み込まずに起こした空のセッション
				// （再起動の直後・一覧から開いた止まったセッション）は、同じ名前で動いていても「手で起こしたもの」ではない
				// （2026-11-22 UseAI で実測: 再起動直後の 2f3fa587 が name 無し・jsonl 無し・needs "send a prompt to start"）。
				// 起きてから transcriptGrace のあいだは様子を見る。過ぎても空なら、止めて一覧から外す（会話が無いので失うものが無い）。
				// そうすると次の run が控えの会話を起こし直す。
				if !hasTranscript(a.SessionID) {
					if time.Since(time.UnixMilli(a.StartedAt)) < transcriptGrace {
						continue
					}
					fmt.Fprintf(w, "%s/%s: 同じ名前で、会話の中身が無いセッション %s が動いているので、止めて一覧から外す", af.ID, sp.Name, short(a.SessionID))
					if dry {
						fmt.Fprintln(w, "（しない）")
						continue
					}
					if _, err := runClaude("stop", a.ID); err != nil {
						fmt.Fprintln(w, " … 止められず失敗:", err)
						continue
					}
					waitStoppedID(a.SessionID, 20*time.Second)
					if _, err := runClaude("rm", short(a.SessionID)); err != nil && !strings.Contains(err.Error(), "No session matching") {
						fmt.Fprintln(w, " … 一覧から外せず:", err)
						continue
					}
					fmt.Fprintln(w, " … した（控えの会話を起こし直す）")
					any = true // 一覧を取り直させる（同じ回の plan が控えの会話を起こし直す）
					continue
				}
			}
			if jobKeepsSettings(a.SessionID, sp.Name) && jobHasTsumugiEnv(a.SessionID, af.ID, sp.Name) {
				if adopting {
					fmt.Fprintf(w, "%s/%s: 紡の外で動いていた %s は紡の設定を持っているので、そのまま配下に入れる\n", af.ID, sp.Name, short(a.SessionID))
					if !dry {
						ss.moveTo(a.SessionID, "手で起こしたものを配下に入れた")
						st.Sessions[sp.Name] = ss
						any = true
					}
				}
				continue
			}
			if adopting {
				fmt.Fprintf(w, "%s/%s: 紡の外で動いている %s を、同じ会話のまま紡の設定で配下に入れる", af.ID, sp.Name, short(a.SessionID))
			} else {
				fmt.Fprintf(w, "%s/%s: 動いている %s に紡の設定が付いていないので、同じ会話のまま付け直す", af.ID, sp.Name, short(a.SessionID))
			}
			if dry {
				fmt.Fprintln(w, "（しない）")
				continue
			}
			if !refitSameID(w, af, sp, a.SessionID, refitNotice, &a) {
				continue
			}
			fmt.Fprintln(w, " … した")
			ss.moveTo(a.SessionID, "手で起こしたものを配下に入れた")
			st.Sessions[sp.Name] = ss
			any = true
		}
		states[af.ID] = st
	}
	return any
}

// job の記録の --settings に、紡の env（TSUMUGI_AFFILIATION・TSUMUGI_SESSION）が入っているか。
// 人が claude -n <名前> で手で起こしたものは、名前が同じでも紡の設定を持たない。
func jobHasTsumugiEnv(sessionID, aff, name string) bool {
	j, ok := readJobRecord(sessionID)
	if !ok {
		return false
	}
	for i, f := range j.RespawnFlags {
		if (f == "--settings" && i+1 < len(j.RespawnFlags) && strings.Contains(j.RespawnFlags[i+1], `"TSUMUGI_SESSION":"`+name+`"`) &&
			strings.Contains(j.RespawnFlags[i+1], `"TSUMUGI_AFFILIATION":"`+aff+`"`)) ||
			strings.HasPrefix(f, "--settings=") && strings.Contains(f, `"TSUMUGI_SESSION":"`+name+`"`) {
			return true
		}
	}
	return false
}

// 起きてからこの時間は、会話の記録がまだ無くても「中身が無い」と決めない（record）。
var transcriptGrace = 3 * time.Minute

// 会話の記録（<Claude の設定の置き場>/projects/<道>/<会話 ID>.jsonl）に中身（人かモデルの発言）があるか。
// ★道は作業フォルダから決まるが、worktree に入ると記録の置き場も移る（2026-11-22 UseAI・Windows で実測）ので、
// projects の下を全部探す。
// ★Claude Code は、会話を読み込まずに「指示を待つ空の会話」を起こすことがある（指示の無いフラグ付きの --resume、
// 一覧から開いた止まったセッション）。そういう会話には記録ファイルが無い。紡が控えの会話 ID を乗り換えるのは、
// 乗り換え先に中身があるときだけにする（2026-11-22 UseAI: 空の会話を useai-accounting として控え直していた）。
// テストで差し替える。
var hasTranscript = func(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	home, err := claudeHome()
	if err != nil {
		return false
	}
	paths, _ := filepath.Glob(filepath.Join(home, "projects", "*", sessionID+".jsonl"))
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if bytes.Contains(line, []byte(`"type":"user"`)) || bytes.Contains(line, []byte(`"type":"assistant"`)) {
				f.Close()
				return true
			}
		}
		f.Close()
	}
	return false
}

// Claude Code の daemon が立ち上がってから、この時間は止まったセッションを --resume しない。
// ★立ち上がった直後（前のセッションを dead と数えた直後）にフラグ無しで --resume すると、続きでなく
// 中身の無い新しい会話が起きる（2026-11-22 Reatex で実測: daemon start の 1 秒後の --resume b80362be が
// 空の 19ed4d08 を起こし、78 秒後の同じ形の --resume は本物の b80362be を戻した）。
// daemon は紡の run の最初の claude 呼び出しで立ち上がることがある（WSL を起こし直したあと）。
var daemonSettle = 2 * time.Minute

// daemon が最後に立ち上がった時刻（<Claude の設定の置き場>/daemon.log の「daemon start」の行）。テストで差し替える。
var daemonStartedAt = readDaemonStartedAt

func readDaemonStartedAt() (time.Time, bool) {
	home, err := claudeHome()
	if err != nil {
		return time.Time{}, false
	}
	f, err := os.Open(filepath.Join(home, "daemon.log"))
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	// 末尾だけ読む（daemon.log は伸び続ける）
	if fi, err := f.Stat(); err == nil && fi.Size() > 256*1024 {
		f.Seek(fi.Size()-256*1024, io.SeekStart)
	}
	var last time.Time
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "daemon start") || !strings.HasPrefix(line, "[") {
			continue
		}
		if i := strings.Index(line, "]"); i > 1 {
			if t, err := time.Parse(time.RFC3339Nano, line[1:i]); err == nil {
				last = t
			}
		}
	}
	return last, !last.IsZero()
}

// daemon が立ち上がったばかりなら、あと何秒待てばよいか（0 なら待たない）。
func daemonJustStarted(now time.Time) time.Duration {
	t, ok := daemonStartedAt()
	if !ok {
		return 0
	}
	if left := t.Add(daemonSettle).Sub(now); left > 0 {
		return left
	}
	return 0
}

// 紡が控えを新しい会話 ID に乗り換えたあと、古い ID を Claude Code の一覧から外す（claude rm）。
// 外さないと、claude の一覧に同じ名前が「Completed」として並び続ける（2026-11-22 UseAI: useai-accounting が 2 つに見えた）。
// claude rm は「安全なら」一覧から外し、その worktree を消す（未 push の変更があれば断る）。会話の記録は残る
// （useai-main が実測: claude rm c7f68470 のあとも jsonl は残った）。古い ID が動いているときは触らない。
func retireOldID(w io.Writer, oldID string, all []agent) {
	if oldID == "" {
		return
	}
	for _, a := range all {
		if a.SessionID == oldID && a.Pid > 0 {
			return
		}
	}
	if _, err := runClaude("rm", short(oldID)); err != nil {
		if strings.Contains(err.Error(), "No session matching") {
			return // もう一覧に無い（Claude Code が片付けた）
		}
		fmt.Fprintf(w, "（古い会話 %s は一覧から外せませんでした: %v）\n", short(oldID), err)
		return
	}
	fmt.Fprintf(w, "（古い会話 %s は claude の一覧から外した・会話の記録は残る）\n", short(oldID))
}

// その会話を claude respawn で起こせば紡の設定が戻るか
// （job の記録が在り、紡が -n で付けた名前と、保存したフラグを覚えている）。
// ★記録が failed（supervisor が落ちている間にプロセスが消えた。WSL を起こし直したあと）でも、名前とフラグが
// 残っていれば claude respawn で戻す。failed の会話をフラグ無しの --resume で起こすと写し・自動の名前・フラグ無しに
// なった（2026-11-22 UseAI: c7f68470 → 75e5d408）が、それは --resume の道（予備のプロセスを使うと新しい ID）の話で、
// claude respawn は daemon の起こし直しの道（(fleet)）なので同じ ID・保存したフラグのまま戻る
// （Reatex の b80362be が再起動後 (fleet) で同じ ID・フラグ付きで戻った。UseAI の be2ad79b を claude respawn して
// 同じ ID のまま戻ることも実測。★failed の記録を claude respawn したことはまだ無い＝次の再起動試験で見る）。
// フラグが空の記録は、覚えている設定が無いので付け直す。
func jobKeepsSettings(sessionID, name string) bool {
	j, ok := readJobRecord(sessionID)
	return ok && j.Name == name && j.NameSource == "user" && len(j.RespawnFlags) > 0
}
