package main

// 更新: Claude Code と、紡が動いている環境（OS のパッケージなど）を新しくし、
// 古い版のまま動いている紡のセッションを、空いているときに新しい版で入れ直す。
//
// ねらい（鈴木さんの方針 2026-11-17）: セキュリティのため、いつも最新を使う。
// Claude Code の更新は純正（claude update・native の自動更新）に任せ、紡は「呼ぶ」「入れ直す」「記録して知らせる」だけ。
//
// 紡.json:
//   "更新": { "いつ": "30 4 * * *", "紡": true, "claude": true, "OS": true, "ほか": [["mise","upgrade"]],
//            "入れ直し": true, "設定項目": true, "知らせる": ["~/bin/notify.sh"] }

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type updateSpec struct {
	Cron      string     `json:"いつ,omitempty"`     // cron 5 項目。無ければ run では更新しない（tsumugi 更新 で手で）
	Self      bool       `json:"紡,omitempty"`      // 紡そのもの（GitHub の Release から）
	Claude    bool       `json:"claude,omitempty"` // claude update
	OS        bool       `json:"OS,omitempty"`     // OS のパッケージ（Linux: apt / macOS: brew / Windows: winget）
	Others    [][]string `json:"ほか,omitempty"`     // ほかに流すコマンド
	Restart   bool       `json:"入れ直し,omitempty"`   // 古い版で動いている、空いているセッションを入れ直す（run のたびに見る）
	Settings  bool       `json:"設定項目,omitempty"`   // Claude Code の設定項目の増減を見る
	Notify    []string   `json:"知らせる,omitempty"`
	SchemaURL string     `json:"設定項目の出どころ,omitempty"`
}

// 手元全体の控え（<手元>/紡の状態.json）。所属をまたぐもの。
type homeState struct {
	LastUpdate string `json:"更新,omitempty"` // 最後に更新した発火時刻
}

const homeStateFile = "紡の状態.json"

func loadHomeState(root string) homeState {
	var h homeState
	if b, err := os.ReadFile(filepath.Join(root, homeStateFile)); err == nil {
		_ = json.Unmarshal(b, &h)
	}
	return h
}

func saveHomeState(root string, h homeState) error {
	b, _ := json.MarshalIndent(h, "", "  ")
	p := filepath.Join(root, homeStateFile)
	if err := os.WriteFile(p+".tmp", append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// run から: 時刻が来ていれば更新する。
func maybeUpdate(w io.Writer, root string, g globalConfig, now time.Time, dry bool) {
	u := g.Update
	if u == nil || u.Cron == "" {
		return
	}
	// 機械が寝ていて時刻を逃しても、起きたあと 24 時間以内なら追いかけて 1 回やる
	// （定時の 2 時間より長く取る。更新は遅れても害が無く、抜けるほうが困る）。
	due := lastDueWithin(u.Cron, now, updateCatchUp)
	if due.IsZero() {
		return
	}
	h := loadHomeState(root)
	at := due.Format(time.RFC3339)
	if h.LastUpdate >= at {
		return
	}
	if !dry {
		unlock, err := lockNamed(root, updateLockFile, 0)
		if err != nil {
			return // ほかの run が更新中
		}
		defer unlock()
		h.LastUpdate = at // 始める前に控える（長い更新の間に、次の run がもう一度始めないように）
		if err := saveHomeState(root, h); err != nil {
			fmt.Fprintln(w, "更新の控えを書けません:", err)
		}
	}
	runUpdate(w, root, *u, dry)
}

const updateCatchUp = 24 * time.Hour

// cron の発火のうち、now から d 以内でいちばん新しいもの。
func lastDueWithin(expr string, now time.Time, d time.Duration) time.Time {
	c, err := parseCron(expr)
	if err != nil {
		return time.Time{}
	}
	now = now.Truncate(time.Minute)
	for t := now; now.Sub(t) <= d; t = t.Add(-time.Minute) {
		if c.match(t) {
			return t
		}
	}
	return time.Time{}
}

// 1 つのコマンドの上限。winget が何時間も返らないことがある（2026-11-21 Windows で約 14 時間）。
var stepTimeout = 2 * time.Hour

// 1 回 更新する。結果は w と <手元>/更新の記録.log に残し、知らせ先へ要点を知らせる。
func runUpdate(w io.Writer, root string, u updateSpec, dry bool) {
	var log bytes.Buffer
	out := io.MultiWriter(w, &log)
	fmt.Fprintf(out, "== 更新 %s（%s）\n", time.Now().Format("2006-01-02 15:04"), thisMachine())
	var notes []string
	if u.Self {
		if tag, err := selfUpdate(out, dry); err != nil {
			notes = append(notes, "★紡の自己更新が失敗: "+err.Error())
			fmt.Fprintln(out, "紡: 失敗:", err)
		} else if tag != "" && !dry {
			notes = append(notes, "紡 "+version()+" → "+tag+"（次の run から新しい版）")
		}
	}
	if u.Claude {
		before := claudeVersion()
		if step(out, dry, "claude", "update") {
			if after := claudeVersion(); after != before {
				notes = append(notes, fmt.Sprintf("Claude Code %s → %s", before, after))
			}
		} else {
			notes = append(notes, "★claude update が失敗")
		}
		fmt.Fprintln(out, "Claude Code:", claudeVersion())
	}
	if u.OS {
		for _, c := range osUpdateCommands() {
			if !step(out, dry, c[0], c[1:]...) {
				notes = append(notes, "★OS の更新が失敗: "+strings.Join(c, " "))
				break
			}
		}
		if _, err := os.Stat("/var/run/reboot-required"); err == nil {
			notes = append(notes, "★OS の再起動が要ります（/var/run/reboot-required）。紡は再起動しません")
			fmt.Fprintln(out, "★再起動が要ります（紡は再起動しない）")
		}
	}
	for _, c := range u.Others {
		if len(c) == 0 {
			continue
		}
		if !step(out, dry, expandHome(c[0]), c[1:]...) {
			notes = append(notes, "★失敗: "+strings.Join(c, " "))
		}
	}
	if u.Settings {
		if diff, err := checkSettings(out, root, u.SchemaURL, dry); err != nil {
			notes = append(notes, "★設定項目を確かめられない: "+err.Error())
		} else if diff != "" {
			notes = append(notes, diff)
		}
	}
	if !dry {
		appendLog(root, log.Bytes())
	}
	if len(notes) > 0 {
		msg := "【紡・更新】" + thisMachine() + "\n" + strings.Join(notes, "\n")
		fmt.Fprintln(w, msg)
		if !dry {
			runNotify(w, u.Notify, msg)
		}
	}
}

// コマンドを 1 つ流す。dry なら出すだけ。
func step(w io.Writer, dry bool, name string, args ...string) bool {
	fmt.Fprintf(w, "$ %s %s\n", name, strings.Join(args, " "))
	if dry {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	b, err := cmd.CombinedOutput()
	tail := string(b)
	if len(tail) > 2000 {
		tail = "…" + tail[len(tail)-2000:]
	}
	fmt.Fprint(w, tail)
	if ctx.Err() == context.DeadlineExceeded {
		fmt.Fprintf(w, "→ %s たっても終わらないので打ち切った\n", stepTimeout)
		return false
	}
	if err != nil {
		fmt.Fprintln(w, "→ 失敗:", err)
		return false
	}
	return true
}

func osUpdateCommands() [][]string {
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("apt-get"); err == nil {
			// sudo -n: パスワードを聞かれる環境では止まらずに失敗させる（知らせる）
			return [][]string{
				{"sudo", "-n", "apt-get", "update", "-q"},
				{"sudo", "-n", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "-q",
					"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "upgrade"},
			}
		}
	case "darwin":
		if _, err := exec.LookPath("brew"); err == nil {
			return [][]string{{"brew", "update"}, {"brew", "upgrade"}}
		}
	case "windows":
		if _, err := exec.LookPath("winget"); err == nil {
			return [][]string{{"winget", "upgrade", "--all", "--silent", "--disable-interactivity",
				"--accept-source-agreements", "--accept-package-agreements"}}
		}
	}
	return nil
}

func claudeVersion() string {
	b, err := runClaude("--version")
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(b))
}

func appendLog(root string, b []byte) {
	f, err := os.OpenFile(filepath.Join(root, "更新の記録.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(b)
}

func runNotify(w io.Writer, cmdline []string, msg string) {
	if len(cmdline) == 0 {
		return
	}
	cmd := exec.Command(expandHome(cmdline[0]), cmdline[1:]...)
	cmd.Stdin = bytes.NewBufferString(msg + "\n")
	cmd.Env = append(os.Environ(), "TSUMUGI_MESSAGE="+msg, "TSUMUGI_IN_RUN=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(w, "知らせられませんでした: %v: %s\n", err, out)
	}
}

// いまの claude の実行ファイルが置かれた時刻（これより前に起きたセッションは古い版で動いている）。
func claudeInstalledAt() (time.Time, error) {
	p, err := exec.LookPath("claude")
	if err != nil {
		return time.Time{}, err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	fi, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, err
	}
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

// 入れ直すもの: 紡が起こした・このマシンの・空いている・claude が置かれる前に起きたセッション。
func staleSessions(machine string, affs []affiliation, states map[string]affState, all []agent, installed time.Time) []agent {
	var out []agent
	for _, af := range affs {
		for _, s := range af.Sessions {
			if !s.here(machine) {
				continue
			}
			ss, ok := states[af.ID].Sessions[s.Name]
			if !ok {
				continue
			}
			a, ok := newestRunning(all, s.Name)
			if !ok || a.SessionID != ss.SessionID || a.Status != "idle" || a.State == "blocked" {
				continue
			}
			if time.UnixMilli(a.StartedAt).Before(installed) {
				out = append(out, a)
			}
		}
	}
	return out
}

// 古い版で動いている空きセッションを、同じ会話のまま新しい版で起こし直す（claude respawn）。
func restartStale(w io.Writer, affs []affiliation, states map[string]affState, all []agent, dry bool) bool {
	installed, err := claudeInstalledAt()
	if err != nil {
		return false
	}
	any := false
	for _, a := range staleSessions(thisMachine(), affs, states, all, installed) {
		fmt.Fprintf(w, "%s: 古い版で動いているので入れ直す", a.Name)
		if dry {
			fmt.Fprintln(w, "（しない）")
			continue
		}
		// 純正の claude respawn に寄せる（2.1.326。同じ会話 ID・同じ名前のまま、いまの版で起こし直す。2026-11-17 実測）
		if _, err := runClaude("respawn", a.ID); err != nil {
			fmt.Fprintln(w, " … 失敗:", err)
			continue
		}
		justResumed(a.Name)
		fmt.Fprintln(w, " … した")
		any = true
	}
	return any
}

func cmdUpdate(args []string) error {
	dry := false
	var only []string
	for _, a := range args {
		switch a {
		case "--dry":
			dry = true
		case "紡", "tsumugi", "claude", "OS", "os", "設定項目", "settings":
			only = append(only, a)
		default:
			return fmt.Errorf("知らない項目です: %s（紡・claude・OS・設定項目）", a)
		}
	}
	root, err := homeDir()
	if err != nil {
		return err
	}
	g, err := loadGlobal(root)
	if err != nil {
		return err
	}
	u := updateSpec{Self: true, Claude: true, OS: true, Settings: true}
	if g.Update != nil {
		u = *g.Update
	}
	if len(only) > 0 {
		v := updateSpec{Notify: u.Notify, SchemaURL: u.SchemaURL}
		for _, o := range only {
			switch o {
			case "紡", "tsumugi":
				v.Self = true
			case "claude":
				v.Claude = true
			case "OS", "os":
				v.OS = true
			case "設定項目", "settings":
				v.Settings = true
			}
		}
		u = v
	}
	if !dry {
		unlock, err := lockHome(root, 2*time.Minute)
		if err != nil {
			return err
		}
		defer unlock()
	}
	runUpdate(os.Stdout, root, u, dry)
	return nil
}
