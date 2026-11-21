package main

// 実際に動いているセッションを Claude Code から取る（claude agents --json）。

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// claude agents --json の 1 行。
type agent struct {
	ID        string `json:"id"` // 短い ID（stop・logs が取る）
	Name      string `json:"name"`
	Kind      string `json:"kind"`   // interactive / background
	Status    string `json:"status"` // busy / idle など
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Pid       int    `json:"pid"` // 動いているものだけ在る
	State     string `json:"state"`
	StartedAt int64  `json:"startedAt"` // ミリ秒
}

// claude を呼ぶ口。テストで差し替える。dir が空なら今の場所で。
var runClaudeIn = func(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("claude", args...)
	cmd.Dir = dir
	// ★呼んだシェルの TSUMUGI_AFFILIATION / TSUMUGI_SESSION を渡さない。紡のセッションの中から
	// tsumugi を打つと、起こしたセッションが呼んだ側の名前を名乗り、チャネルのソケットや結の名乗りを
	// 取り違えた（2026-11-20 Reatex の実測）。起こすセッションの名前は --settings の env で渡す。
	cmd.Env = cleanEnv(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("claude %v が失敗しました: %w: %s", args, err, ee.Stderr)
		}
		return nil, fmt.Errorf("claude %v を起動できません: %w", args, err)
	}
	return out, nil
}

func runClaude(args ...string) ([]byte, error) { return runClaudeIn("", args...) }

func cleanEnv(env []string) []string {
	out := env[:0:0]
	for _, e := range env {
		if strings.HasPrefix(e, "TSUMUGI_AFFILIATION=") || strings.HasPrefix(e, "TSUMUGI_SESSION=") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// 動いているものだけ。
func listAgents() ([]agent, error) {
	out, err := runClaude("agents", "--json")
	if err != nil {
		return nil, err
	}
	return parseAgents(out)
}

func parseAgents(b []byte) ([]agent, error) {
	var as []agent
	if err := json.Unmarshal(b, &as); err != nil {
		return nil, fmt.Errorf("claude agents --json の出力を読めません: %w", err)
	}
	return as, nil
}

// 終わったものも含めて全部。
func listAllAgents() ([]agent, error) {
	out, err := runClaude("agents", "--json", "--all")
	if err != nil {
		return nil, err
	}
	return parseAgents(out)
}
