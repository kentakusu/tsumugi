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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type jobRecord struct {
	Name       string `json:"name"`
	NameSource string `json:"nameSource"`
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

// その会話を、フラグ無しの --resume で起こしても紡の設定が戻るか
// （job の記録が在り、紡が -n で付けた名前を覚えている）。
func jobKeepsSettings(sessionID, name string) bool {
	j, ok := readJobRecord(sessionID)
	return ok && j.Name == name && j.NameSource == "user"
}
