package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tsumugi 外す: 止めて、いまの会話と前の会話を claude rm し、所属.json の項と控えを消す（ほかの項・鍵はそのまま）。
func TestRemoveSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TSUMUGI_HOME", dir)
	t.Setenv("TSUMUGI_MACHINE", "m")
	os.MkdirAll(filepath.Join(dir, "a"), 0o700)
	p := filepath.Join(dir, "a", configFile)
	os.WriteFile(p, []byte(`{"表示名":"a","セッション":[{"名前":"s","作業フォルダ":"/w/s"},{"名前":"keep","作業フォルダ":"/w/k"}],"見張り":{"長すぎ":"3h"}}`), 0o600)
	af := affiliation{ID: "a", Path: p, Sessions: []sessionSpec{{Name: "s"}, {Name: "keep"}}}
	saveState(af, affState{Sessions: map[string]sessionState{
		"s":    {SessionID: "S1", Previous: []prevSession{{ID: "P1"}}},
		"keep": {SessionID: "K1"},
	}})
	all := []agent{
		{Name: "s", SessionID: "S1", ID: "s1000000", Pid: 1, Status: "idle"},
		{Name: "old", SessionID: "P1", ID: "p1000000", State: "done"},
		{Name: "keep", SessionID: "K1", ID: "k1000000", Pid: 2, Status: "idle"},
	}
	f := &fakeClaude{}
	f.install(t)
	var out bytes.Buffer
	if err := removeSession(&out, []affiliation{af}, "s", false, all); err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, c := range f.calls {
		if c[0] != "agents" {
			calls = append(calls, strings.Join(c, " "))
		}
	}
	if strings.Join(calls, " | ") != "stop s1000000 | rm s1000000 | rm p1000000" {
		t.Fatalf("呼び方が違う: %q", calls)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), `"s"`) || !strings.Contains(string(b), `"keep"`) || !strings.Contains(string(b), "見張り") {
		t.Fatalf("所属.json の消し方が違う: %s", b)
	}
	st, _ := loadState(af)
	if _, ok := st.Sessions["s"]; ok || st.Sessions["keep"].SessionID != "K1" {
		t.Fatalf("控えの消し方が違う: %+v", st.Sessions)
	}
	if !strings.Contains(out.String(), `"S1"`) {
		t.Fatalf("戻し方（引き継ぐ会話）を出していない: %s", out.String())
	}
}

// tsumugi 止める: 控えの会話 ID で claude stop し、控えは残す。作業中は断る（--強制 なら止める）。
// --しばらく の印があるあいだは、run（常駐）・定時で起こさず、届けは溜める。一覧には「止めてある」で残る。tsumugi a の口で印は外れる。
func TestStopSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TSUMUGI_HOME", dir)
	t.Setenv("TSUMUGI_MACHINE", "m")
	os.MkdirAll(filepath.Join(dir, "a"), 0o700)
	sc := []scheduleSpec{{Cron: "* * * * *", Prompt: "巡回"}}
	af := affiliation{ID: "a", Path: filepath.Join(dir, "a", configFile), Sessions: []sessionSpec{{Name: "s", Schedules: sc}, {Name: "other"}}}
	saveState(af, affState{Sessions: map[string]sessionState{"s": {SessionID: "S1"}}})
	affs := []affiliation{af}
	f := &fakeClaude{}
	f.install(t)

	// 作業中は断る
	busy := []agent{{Name: "s", SessionID: "S1", ID: "s1000000", Pid: 1, Status: "busy"}, {Name: "s", SessionID: "S0", ID: "s0000000", Pid: 2, Status: "idle"}}
	var out bytes.Buffer
	if err := stopSession(&out, affs, "s", true, false, busy); err == nil || !strings.Contains(err.Error(), "作業中") {
		t.Fatalf("作業中を断っていない: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("断ったのに claude を呼んだ: %q", f.calls)
	}
	// 空いていれば、控えの会話 ID（同じ名前の別の会話 S0 ではなく）で止め、印を付ける
	idle := []agent{{Name: "s", SessionID: "S1", ID: "s1000000", Pid: 1, Status: "idle"}, {Name: "s", SessionID: "S0", ID: "s0000000", Pid: 2, Status: "idle"}}
	out.Reset()
	if err := stopSession(&out, affs, "s", true, false, idle); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) == 0 || strings.Join(f.calls[0], " ") != "stop s1000000" {
		t.Fatalf("控えの会話で止めていない: %q", f.calls)
	}
	st, _ := loadState(af)
	if !st.Sessions["s"].Held || st.Sessions["s"].SessionID != "S1" {
		t.Fatalf("印・控えが違う: %+v", st.Sessions["s"])
	}
	states := map[string]affState{"a": st}
	// 印のあいだ: run は起こさない・定時は渡さない
	for _, a := range plan(affs, states, nil) {
		if a.Name == "s" {
			t.Fatalf("止めておく印があるのに起こそうとした: %+v", a)
		}
	}
	for _, d := range dueDeliveries("m", affs, states, nil, time.Now()) {
		if d.Name == "s" {
			t.Fatalf("止めておく印があるのに定時を渡そうとした: %+v", d)
		}
	}
	// 一覧に残る
	var b bytes.Buffer
	printListFull(&b, "m", affs, states, nil)
	if !strings.Contains(b.String(), "止めてある（tsumugi a で起こす）") || !strings.Contains(b.String(), "S1") {
		t.Fatalf("一覧に止めてあると出ていない:\n%s", b.String())
	}
	// 届けは溜めておく
	if _, err := enqueue(af, "s", "あとで", time.Now()); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if ok, err := deliverQueue(&out, affs, states, nil, time.Now(), false); err != nil || ok {
		t.Fatalf("止めておく印があるのに届けを渡した: %v %v %s", ok, err, out.String())
	}
	if qs, _ := loadQueue(af); len(qs) != 1 || !strings.Contains(out.String(), "溜めておく") {
		t.Fatalf("届けを溜めていない: %d %s", len(qs), out.String())
	}
	// tsumugi a の口で印が外れる（対照: 外れたあとは run が起こす）
	if released, err := releaseHold(af, "s"); err != nil || !released {
		t.Fatalf("印を外せない: %v %v", released, err)
	}
	st, _ = loadState(af)
	states = map[string]affState{"a": st}
	found := false
	for _, a := range plan(affs, states, nil) {
		if a.Name == "s" {
			found = true
		}
	}
	if !found {
		t.Fatal("印を外したのに起こさない")
	}
}
