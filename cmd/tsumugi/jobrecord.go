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

// 紡が控えた会話が動いているのに、Claude Code 側の名前が紡の名前と違う（自動で付け替わった）ものを知らせる。
// そうなったセッションは、紡の設定（チャネル・権限モード・env・結の決まり）を持たずに起きている恐れがある。
// 紡は動いているものを止めない。止めれば、次の run が設定を付け直して起こす。
func warnLostSettings(w io.Writer, machine string, affs []affiliation, states map[string]affState, all []agent) {
	for _, af := range affs {
		for _, sp := range af.Sessions {
			ss, known := states[af.ID].Sessions[sp.Name]
			if !known || ss.SessionID == "" || !sp.here(machine) {
				continue
			}
			for _, a := range all {
				if a.Pid <= 0 || a.SessionID != ss.SessionID {
					continue
				}
				j, ok := readJobRecord(ss.SessionID)
				if ok && (j.Name != sp.Name || j.NameSource != "user") {
					fmt.Fprintf(w, "★%s/%s: 動いていますが、Claude Code 側の名前が %q（%s）になっていて、紡の設定（チャネル・権限モード・env）が付いていない恐れがあります。"+
						"空いているときに claude stop %s すれば、次の run が設定を付け直して起こします\n", af.ID, sp.Name, j.Name, j.NameSource, a.ID)
				}
				break
			}
		}
	}
}

// 会話の記録（<Claude の設定の置き場>/projects/<道>/<会話 ID>.jsonl）に中身（人かモデルの発言）があるか。
// ★道は作業フォルダから決まるが、worktree に入ると記録の置き場も移る（2026-11-22 UseAI・Windows で実測）ので、
//
//	projects の下を全部探す。
//
// ★Claude Code は、会話を読み込まずに「指示を待つ空の会話」を起こすことがある（指示の無いフラグ付きの --resume、
//
//	一覧から開いた止まったセッション）。そういう会話には記録ファイルが無い。紡が控えの会話 ID を乗り換えるのは、
//	乗り換え先に中身があるときだけにする（2026-11-22 UseAI: 空の会話を useai-accounting として控え直していた）。
//
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

// その会話を、フラグ無しの --resume で起こしても紡の設定が戻るか
// （job の記録が在り、紡が -n で付けた名前を覚えている）。
// ★名前が残っていても、記録が failed（supervisor が落ちている間にプロセスが消えた等）だと、Claude Code は
// その記録の設定を使わずに写しを作り、名前を自動で付け、フラグ無しで起こす（2026-11-22 UseAI で実測:
// useai-accounting の c7f68470 は name・nameSource: user・respawnFlags が残ったまま state: failed で、
// フラグ無し＋定時の指示で渡すと写し 75e5d408 が auto の名前・respawnFlags 空で起きた）。
// フラグが空の記録も、覚えている設定が無い。どちらも付け直す側に倒す。
func jobKeepsSettings(sessionID, name string) bool {
	j, ok := readJobRecord(sessionID)
	return ok && j.Name == name && j.NameSource == "user" && j.State != "failed" && len(j.RespawnFlags) > 0
}
