package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// tsumugi 新しく: 動いているセッションに、チャネルで申し送りを頼み、busy → idle になるのを待ってから、
// 止めて一覧から外し、「前の会話から新しく起こし直した」知らせ付きの最初の指示で新しい会話を起こす。
func TestRenewWithHandoff(t *testing.T) {
	dir, err := os.MkdirTemp(sockTempBase(), "tsrn")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("TSUMUGI_HOME", dir)
	t.Setenv("TSUMUGI_MACHINE", "m")
	os.MkdirAll(filepath.Join(dir, "a"), 0o700)
	af := affiliation{ID: "a", Path: filepath.Join(dir, "a", configFile), Sessions: []sessionSpec{{Name: "s", Prompt: "最初の指示"}}}
	saveState(af, affState{Sessions: map[string]sessionState{"s": {SessionID: "OLD-1"}}})
	oldWait, oldPoll, oldRenew := handoffWait, handoffPoll, renewWait
	t.Cleanup(func() { handoffWait, handoffPoll, renewWait = oldWait, oldPoll, oldRenew })
	handoffWait, handoffPoll, renewWait = 10*time.Second, 50*time.Millisecond, 5*time.Second

	// いまのセッション: チャネルで受け取ったら、しばらく busy、そのあと idle
	var mu sync.Mutex
	status := "idle"
	stopped, started := false, false
	got := make(chan string, 1)
	go channelListen("a", "s", func(v any) {
		got <- fmt.Sprint(v.(map[string]any)["params"])
		mu.Lock()
		status = "busy"
		mu.Unlock()
		go func() {
			time.Sleep(200 * time.Millisecond)
			mu.Lock()
			status = "idle"
			mu.Unlock()
		}()
	})
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(channelSock(dir, "a", "s")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var calls [][]string
	oldRun := runClaudeIn
	t.Cleanup(func() { runClaudeIn = oldRun })
	runClaudeIn = func(d string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, args)
		switch args[0] {
		case "stop":
			stopped = true
		case "--bg":
			started = true
		case "agents":
			var rows []string
			if !stopped {
				rows = append(rows, fmt.Sprintf(`{"name":"s","sessionId":"OLD-1","id":"old00000","pid":1,"status":%q}`, status))
			}
			if started {
				rows = append(rows, `{"name":"s","sessionId":"NEW-1","id":"new00000","pid":2,"status":"busy","startedAt":5}`)
			}
			return []byte("[" + strings.Join(rows, ",") + "]"), nil
		}
		return nil, nil
	}
	var out bytes.Buffer
	if err := askHandoff(&out, dir, []affiliation{af}, "s"); err != nil {
		t.Fatalf("申し送りで止まった: %v\n%s", err, out.String())
	}
	select {
	case p := <-got:
		if !strings.Contains(p, "結の現在地に書いて") {
			t.Fatalf("申し送りの頼みが違う: %s", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("チャネルで申し送りを頼んでいない")
	}
	if !strings.Contains(out.String(), "申し送りが済みました") {
		t.Fatalf("書き終えるのを待っていない: %s", out.String())
	}
	if err := renewSession(&out, []affiliation{af}, "s"); err != nil {
		t.Fatal(err)
	}
	var seq []string
	var bg []string
	for _, c := range calls {
		if c[0] == "agents" {
			continue
		}
		seq = append(seq, c[0]+" "+c[1])
		if c[0] == "--bg" {
			bg = c
		}
	}
	if len(seq) != 3 || seq[0] != "stop old00000" || seq[1] != "rm old00000" || !strings.HasPrefix(seq[2], "--bg") {
		t.Fatalf("止める・外す・起こすの順でない: %q", seq)
	}
	last := bg[len(bg)-1]
	if !strings.HasPrefix(last, "【紡】tsumugi 新しく で、前の会話（OLD-1）から新しい会話に起こし直しました。") || !strings.HasSuffix(last, "最初の指示") {
		t.Fatalf("新しい会話の最初の指示が違う: %q", last)
	}
	for _, a := range bg {
		if a == "--resume" {
			t.Fatalf("新しく なのに前の会話を --resume した: %q", bg)
		}
	}
	st, _ := loadState(af)
	ss := st.Sessions["s"]
	if ss.SessionID != "NEW-1" || len(ss.Previous) != 1 || ss.Previous[0].ID != "OLD-1" || ss.Previous[0].Why != "tsumugi 新しく" {
		t.Fatalf("控えが違う: %+v", ss)
	}
}

// 作業中のセッションは、申し送りを頼まずに断る（--すぐ でなければ）。
func TestRenewRefusesBusy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TSUMUGI_HOME", dir)
	os.MkdirAll(filepath.Join(dir, "a"), 0o700)
	af := affiliation{ID: "a", Path: filepath.Join(dir, "a", configFile), Sessions: []sessionSpec{{Name: "s"}}}
	saveState(af, affState{Sessions: map[string]sessionState{"s": {SessionID: "OLD-1"}}})
	f := &fakeClaude{}
	f.install(t)
	f.started = []agent{{Name: "s", SessionID: "OLD-1", ID: "old00000", Pid: 1, Status: "busy"}}
	var out bytes.Buffer
	if err := askHandoff(&out, dir, []affiliation{af}, "s"); err == nil || !strings.Contains(err.Error(), "作業中") {
		t.Fatalf("作業中を断っていない: %v", err)
	}
}
