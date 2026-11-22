package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 乗り換えると前の会話に残る。同じ ID・空には乗り換えない。
func TestMoveTo(t *testing.T) {
	var ss sessionState
	if !ss.moveTo("A", "x") || ss.SessionID != "A" || len(ss.Previous) != 0 {
		t.Fatalf("初めての控えは前の会話を作らない: %+v", ss)
	}
	if ss.moveTo("A", "x") || ss.moveTo("", "x") {
		t.Fatal("同じ ID・空に乗り換えた")
	}
	if !ss.moveTo("B", "写し") || ss.SessionID != "B" || len(ss.Previous) != 1 || ss.Previous[0].ID != "A" || ss.Previous[0].Why != "写し" {
		t.Fatalf("前の会話に残っていない: %+v", ss)
	}
}

// Completed のうち、紡が過去に管理していたもの（前の会話・同じ名前の古い会話）だけを claude rm で片付ける。
// いまの控えの会話（止まっている常駐しないもの）・動いているもの・紡と関係の無いものには触らない。
func TestCleanupCompleted(t *testing.T) {
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{{Name: "main"}, {Name: "od", Resident: boolp(false)}}}}
	states := map[string]affState{"x": {Sessions: map[string]sessionState{
		"main": {SessionID: "NOW", Previous: []prevSession{{ID: "PREV", Until: "2026-11-22T10:00:00Z", Why: "写し"}}},
		"od":   {SessionID: "OD"},
	}}}
	all := []agent{
		{Name: "main", SessionID: "NOW", ID: "now", Pid: 1, Status: "idle"},                   // いま（動いている）
		{Name: "lw-monitoring-patrol", SessionID: "PREV", ID: "prev", State: "done"},          // 前の会話（自動の名前）
		{Name: "main", SessionID: "OLDMAIN", ID: "oldmain", State: "failed", StartedAt: 1000}, // 同じ名前の古い会話
		{Name: "od", SessionID: "OD", ID: "od", State: "stopped"},                             // いまの控え（常駐しない・止まっている）
		{Name: "someone", SessionID: "HAND", ID: "hand", State: "done"},                       // 紡と関係の無いもの
	}
	f := &fakeClaude{}
	f.install(t)
	var out bytes.Buffer
	if !cleanupCompleted(&out, "m", affs, states, all, false) {
		t.Fatalf("片付けていない: %s", out.String())
	}
	var rms []string
	for _, c := range f.calls {
		if c[0] == "rm" {
			rms = append(rms, c[1])
		}
	}
	if strings.Join(rms, ",") != "prev,oldmain" {
		t.Fatalf("片付けたものが違う: %q\n%s", rms, out.String())
	}
	prev := states["x"].Sessions["main"].Previous
	if len(prev) != 2 || prev[1].ID != "OLDMAIN" || prev[1].Why != "同じ名前の古い会話" {
		t.Fatalf("同じ名前の古い会話を前の会話に残していない: %+v", prev)
	}
}

// 所属.json に無い名前で動いている背景セッションを、その機の所属に「常駐しない」で足す。
// 画面で開いているもの・中身の無いもの・紡の控えに在るもの・所属.json の名前のものは足さない。
func TestAdoptUnknown(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "x"), 0o700)
	p := filepath.Join(dir, "x", configFile)
	os.WriteFile(p, []byte(`{"表示名":"x","セッション":[{"名前":"main","作業フォルダ":"/w/main"}]}`), 0o600)
	stubTranscripts(t, "HAND", "SCREEN", "MAIN")
	affs := []affiliation{{ID: "x", Path: p, Sessions: []sessionSpec{{Name: "main", Dir: "/w/main"}}}}
	states := map[string]affState{"x": {Sessions: map[string]sessionState{"main": {SessionID: "MAIN"}}}}
	all := []agent{
		{Name: "main", SessionID: "MAIN", ID: "main0000", Pid: 1, Kind: "background"},
		{Name: "my-work", SessionID: "HAND", ID: "hand0000", Pid: 2, Kind: "background", Cwd: "/w/other"},
		{Name: "screen", SessionID: "SCREEN", ID: "scr00000", Pid: 3, Kind: "interactive", Cwd: "/w/x"},
		{Name: "empty", SessionID: "EMPTY", ID: "emp00000", Pid: 4, Kind: "background", Cwd: "/w/y"},
	}
	var out bytes.Buffer
	if !adoptUnknown(&out, "m", affs, states, all, false) {
		t.Fatalf("足していない: %s", out.String())
	}
	if len(affs[0].Sessions) != 2 || affs[0].Sessions[1].Name != "my-work" || affs[0].Sessions[1].resident() {
		t.Fatalf("所属に常駐しないで足していない: %+v", affs[0].Sessions)
	}
	if states["x"].Sessions["my-work"].SessionID != "HAND" {
		t.Fatalf("控えに入れていない: %+v", states)
	}
	b, _ := os.ReadFile(p)
	var doc struct {
		Sessions []map[string]any `json:"セッション"`
	}
	json.Unmarshal(b, &doc)
	if len(doc.Sessions) != 2 || doc.Sessions[1]["名前"] != "my-work" || doc.Sessions[1]["常駐"] != false || doc.Sessions[1]["作業フォルダ"] != "/w/other" || doc.Sessions[0]["名前"] != "main" {
		t.Fatalf("所属.json の書き方が違う: %s", b)
	}
	if strings.Contains(out.String(), "screen") || strings.Contains(out.String(), "empty") {
		t.Fatalf("画面・空の会話を足した: %s", out.String())
	}
}

// 足す所属: その機に所属が 1 つならそれ。複数なら作業フォルダがいちばん長く重なる所属。決まらなければ -1。
func TestPickAffiliation(t *testing.T) {
	one := []affiliation{{ID: "a", Sessions: []sessionSpec{{Name: "s", Dir: "/w/a"}}}}
	if pickAffiliation("m", one, "/anywhere") != 0 {
		t.Fatal("所属が 1 つならそれのはず")
	}
	two := []affiliation{
		{ID: "a", Sessions: []sessionSpec{{Name: "s", Dir: "/w"}}},
		{ID: "b", Sessions: []sessionSpec{{Name: "t", Dir: "/w/b"}}},
	}
	if pickAffiliation("m", two, "/w/b/sub") != 1 || pickAffiliation("m", two, "/w/c") != 0 || pickAffiliation("m", two, "/z") != -1 {
		t.Fatal("作業フォルダの重なりで選んでいない")
	}
}

// tsumugi l: 控えの会話 ID で claude agents と突き合わせ（名前が違えば★）、紡の外・前の会話も全部並べる。
func TestListFull(t *testing.T) {
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{{Name: "main"}, {Name: "od", Resident: boolp(false)}}}}
	states := map[string]affState{"x": {Sessions: map[string]sessionState{
		"main": {SessionID: "NOW-0000-1111", Previous: []prevSession{{ID: "PREV"}}},
	}}}
	all := []agent{
		{Name: "lw-monitoring-patrol", SessionID: "NOW-0000-1111", ID: "now00000", Pid: 1, Status: "idle"},
		{Name: "main", SessionID: "OTHER", ID: "other000", State: "done", StartedAt: 5}, // 名前は同じだが控えではない
		{Name: "old-copy", SessionID: "PREV", ID: "prev0000", State: "done"},
		{Name: "my-work", SessionID: "HAND", ID: "hand0000", Pid: 2, Status: "busy", Kind: "interactive", Cwd: "/w"},
	}
	var b bytes.Buffer
	printListFull(&b, "m", affs, states, all)
	out := b.String()
	lines := strings.Split(out, "\n")
	if !strings.Contains(lines[1], "now00000") || !strings.Contains(lines[1], "★lw-monitoring-patrol") || !strings.Contains(lines[1], "idle") {
		t.Fatalf("控えの会話 ID で突き合わせていない:\n%s", out)
	}
	for _, want := range []string{"main（前の会話）", "prev0000", "（紡の外）", "my-work", "busy・画面", "other000"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q が出ていない:\n%s", want, out)
		}
	}
}

// tsumugi tree: いまの会話と前の会話を木の形で、紡の外は作業フォルダごとに並べる。
func TestTree(t *testing.T) {
	affs := []affiliation{{ID: "x", Label: "テスト", Sessions: []sessionSpec{{Name: "main", Dir: "/w/main"}}}}
	until := time.Date(2026, 11, 22, 19, 44, 0, 0, time.Local).Format(time.RFC3339)
	states := map[string]affState{"x": {Sessions: map[string]sessionState{
		"main": {SessionID: "NOW00000-1", Previous: []prevSession{{ID: "OLD00000-1", Until: until, Why: "写しになった"}}},
	}}}
	all := []agent{
		{Name: "main", SessionID: "NOW00000-1", ID: "now00000", Pid: 1, Status: "idle"},
		{Name: "x", SessionID: "OUT", ID: "out00000", Cwd: "/w/other", State: "done"},
	}
	var b bytes.Buffer
	printTree(&b, "m", affs, states, all)
	out := b.String()
	for _, want := range []string{"x（テスト）", "└─ main", "いま  NOW00000", "idle", "前    OLD00000", "11-22 19:44 まで・写しになった・一覧に無い", "（紡の外・作業フォルダごと）", "/w/other", "out00000"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q が出ていない:\n%s", want, out)
		}
	}
}
