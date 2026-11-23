package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 申し送りの頼みは、届いた時点で会話の記録に書かれる。取りかかる前の idle を「済んだ」と読まない
// （2026-11-23 UseAI: 3 秒で済んだと判断し、取りかかって busy になったところで「作業中」と断って止まった）。
func TestHandoffWaitsForWork(t *testing.T) {
	for _, c := range []struct {
		name      string
		busyAfter time.Duration // 頼みを受けてから busy になるまで（0 なら busy にならない）
		enters    bool          // 頼みが会話の記録に入るか
		wantErr   bool
		wantOut   string
		minWait   time.Duration
	}{
		{"取りかかるのが遅くても、busy を見てから済んだとする", 400 * time.Millisecond, true, false, "申し送りが済みました", 600 * time.Millisecond},
		{"busy を見ないまま空いていても、頼みが入っていれば済んだとする", 0, true, false, "済んだとみなします", time.Second},
		{"頼みが入らなければ止める", 0, false, true, "", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, err := os.MkdirTemp(sockTempBase(), "tshw")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			t.Setenv("TSUMUGI_HOME", dir)
			t.Setenv("TSUMUGI_MACHINE", "m")
			os.MkdirAll(filepath.Join(dir, "a"), 0o700)
			af := affiliation{ID: "a", Path: filepath.Join(dir, "a", configFile), Sessions: []sessionSpec{{Name: "s", Prompt: "最初の指示"}}}
			saveState(af, affState{Sessions: map[string]sessionState{"s": {SessionID: "OLD-1"}}})
			oldWait, oldPoll, oldNoBusy, oldQuiet, oldCount := handoffWait, handoffPoll, handoffNoBusy, quietFor, channelEnqueued
			t.Cleanup(func() {
				handoffWait, handoffPoll, handoffNoBusy, quietFor, channelEnqueued = oldWait, oldPoll, oldNoBusy, oldQuiet, oldCount
			})
			handoffWait, handoffPoll, handoffNoBusy = 5*time.Second, 50*time.Millisecond, time.Second
			// 記録はいつも「いま書かれたばかり」（頼みが届いた時点で書かれる、を真似る）
			quietFor = func(string, time.Time) (time.Duration, bool) { return 0, true }
			var entered int32
			channelEnqueued = func(string) int { return int(atomic.LoadInt32(&entered)) }

			var mu sync.Mutex
			status := "idle"
			go channelListen("a", "s", func(any) {
				if c.enters {
					atomic.AddInt32(&entered, 1)
				}
				if c.busyAfter > 0 {
					go func() {
						time.Sleep(c.busyAfter)
						mu.Lock()
						status = "busy"
						mu.Unlock()
						time.Sleep(200 * time.Millisecond)
						mu.Lock()
						status = "idle"
						mu.Unlock()
					}()
				}
			})
			for i := 0; i < 50; i++ {
				if _, err := os.Stat(channelSock(dir, "a", "s")); err == nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			oldRun := runClaudeIn
			t.Cleanup(func() { runClaudeIn = oldRun })
			runClaudeIn = func(d string, args ...string) ([]byte, error) {
				if args[0] == "agents" {
					mu.Lock()
					defer mu.Unlock()
					return []byte(fmt.Sprintf(`[{"name":"s","sessionId":"OLD-1","id":"old00000","pid":1,"status":%q}]`, status)), nil
				}
				return nil, nil
			}
			var out bytes.Buffer
			t0 := time.Now()
			err = askHandoff(&out, dir, []affiliation{af}, "s")
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v（止めるはず=%v）: %s", err, c.wantErr, out.String())
			}
			if c.wantOut != "" && !strings.Contains(out.String(), c.wantOut) {
				t.Fatalf("%q が出ていない: %s", c.wantOut, out.String())
			}
			if el := time.Since(t0); el < c.minWait {
				t.Fatalf("早すぎる（%v）＝取りかかる前に済んだと読んだ: %s", el, out.String())
			}
		})
	}
}
