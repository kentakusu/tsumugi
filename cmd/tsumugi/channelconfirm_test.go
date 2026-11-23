package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 会話の記録から、紡のチャネルで届いた指示を JSON として数える（素の文字列や自分の引用には当たらない）。
func TestChannelEnqueued(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	dir := filepath.Join(home, "projects", "D--x")
	os.MkdirAll(dir, 0o700)
	lines := []string{
		`{"type":"queue-operation","operation":"enqueue","content":"<channel source=\"plugin:tsumugi-channel:tsumugi\" from=\"tsumugi\">\n【定時】朝</channel>"}`,
		`{"type":"queue-operation","operation":"dequeue","content":"<channel source=\"plugin:tsumugi-channel:tsumugi\">x"}`,              // 取り出しは数えない
		`{"type":"queue-operation","operation":"enqueue","content":"<cross-session-message>… <channel source=\"plugin:tsumugi-channel"}`, // 別の口（本文に引用があるだけ）
		`{"type":"assistant","message":{"content":"<channel source=\"plugin:tsumugi-channel:tsumugi\"> と書いた"}}`,                          // 自分の発言の引用
		`{"type":"queue-operation","operation":"enqueue","content":"<channel source=\"plugin:tsumugi-channel:tsumugi\">\n【届け】2</channel>"}`,
	}
	os.WriteFile(filepath.Join(dir, "S1.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	if got := channelEnqueued("S1"); got != 2 {
		t.Fatalf("2 件のはず: %d", got)
	}
	if got := channelEnqueued("S2"); got != -1 {
		t.Fatalf("記録が無ければ -1 のはず: %d", got)
	}
}

// claude respawn の直後にチャネルで渡すと、ソケットは ok を返すのに捨てられることがある（2026-11-23 UseAI）。
// 会話の記録に入ったかを数え、入らなければ渡し直す。何度渡しても入らなければ「渡せなかった」として残す。
func TestRespawnDeliverConfirms(t *testing.T) {
	for _, c := range []struct {
		name     string
		drop     int32 // 最初の何通を捨てるか
		wantOK   bool
		wantSent int32
	}{
		{"1 通目が捨てられたら渡し直して入る", 1, true, 2},
		{"最初から入る（対照）", 0, true, 1},
		{"何度渡しても入らない", 99, false, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, err := os.MkdirTemp(sockTempBase(), "tscf")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			t.Setenv("TSUMUGI_HOME", dir)
			os.WriteFile(filepath.Join(dir, globalFile), []byte(`{"チャネル":true}`), 0o600)
			for _, v := range []*time.Duration{&respawnWait, &channelConfirm} {
				old := *v
				t.Cleanup(func() { *v = old })
			}
			respawnWait, channelConfirm = 5*time.Second, 2*time.Second
			oldTries := channelTries
			channelTries = 2
			t.Cleanup(func() { channelTries = oldTries })
			stubJobRecords(t, map[string]jobRecord{"S1": {Name: "s", NameSource: "user", State: "done", RespawnFlags: []string{"--channels", "-n"}}})
			var sent, entered int32
			oldCount := channelEnqueued
			t.Cleanup(func() { channelEnqueued = oldCount })
			channelEnqueued = func(string) int { return int(atomic.LoadInt32(&entered)) }
			running := false
			oldRun := runClaudeIn
			t.Cleanup(func() { runClaudeIn = oldRun })
			runClaudeIn = func(d string, args ...string) ([]byte, error) {
				switch args[0] {
				case "respawn":
					running = true
					go channelListen("a", "s", func(any) {
						if n := atomic.AddInt32(&sent, 1); n > c.drop {
							atomic.AddInt32(&entered, 1)
						}
					})
				case "agents":
					if running {
						return []byte(`[{"name":"s","sessionId":"S1","id":"S1","pid":9,"status":"idle"}]`), nil
					}
					return []byte("[]"), nil
				}
				return nil, nil
			}
			var out bytes.Buffer
			ok, _ := deliverPrompt(&out, delivery{Affiliation: "a", Name: "s", SessionID: "S1", Prompt: "【定時】朝", spec: sessionSpec{Name: "s"}})
			if ok != c.wantOK || atomic.LoadInt32(&sent) != c.wantSent {
				t.Fatalf("ok=%v 渡した=%d（%v・%d のはず）: %s", ok, sent, c.wantOK, c.wantSent, out.String())
			}
			if ok && !strings.Contains(out.String(), "会話の記録に入ったのを確かめた") {
				t.Fatalf("確かめたと出していない: %s", out.String())
			}
		})
	}
}
