package main

// チャネルで渡したものが、本当に会話に入ったかを会話の記録で数える。
//
// ★チャネルのソケットが「ok」を返しても、届いた証拠にならない（2026-11-23 UseAI の useai-main が実測:
// claude respawn した直後に渡すと、ソケット（起きたプロセスの子の tsumugi channel）は ok を返すが、
// セッションがまだターンを受け取れる状態になっておらず、通知が捨てられた。9:00 の定時が届かなかった）。
// 届いたものは、会話の記録（projects/<道>/<会話 ID>.jsonl）に
// {"type":"queue-operation","operation":"enqueue","content":"<channel source=\"plugin:tsumugi-channel…"} の行として残る。
// ★本文を素の文字列で grep しない（記録の中では JSON として逃がされる・自分の発言に引用が当たる）。行を JSON として読む。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// 会話の記録にある、紡のチャネルから届いた指示の数。記録が見つからなければ -1。テストで差し替える。
var channelEnqueued = func(sessionID string) int {
	if sessionID == "" {
		return -1
	}
	home, err := claudeHome()
	if err != nil {
		return -1
	}
	paths, _ := filepath.Glob(filepath.Join(home, "projects", "*", sessionID+".jsonl"))
	if len(paths) == 0 {
		return -1
	}
	n := 0
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if !bytes.Contains(line, []byte(`"queue-operation"`)) {
				continue
			}
			var r struct {
				Type, Operation, Content string
			}
			if json.Unmarshal(line, &r) != nil {
				continue
			}
			if r.Type == "queue-operation" && r.Operation == "enqueue" && strings.HasPrefix(r.Content, `<channel source="plugin:tsumugi-channel`) {
				n++
			}
		}
		f.Close()
	}
	return n
}
