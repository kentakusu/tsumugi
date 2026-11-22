package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 起こし直す: Claude Code が紡の名前を覚えていれば今までどおりフラグ無し。
// 記録が無い・名前が自動で付け替わったときは、紡のフラグを全部付けて --resume する（指示は付けない）。
func TestPlanRefitWhenJobForgot(t *testing.T) {
	stubJobRecords(t, map[string]jobRecord{
		"s-keep": {Name: "keep", NameSource: "user"},
		"s-auto": {Name: "cc-34-migration-handoff", NameSource: "auto"},
	})
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{
		{Name: "keep", Prompt: "最初の指示", Permission: "bypassPermissions"},
		{Name: "auto", Prompt: "最初の指示", Permission: "bypassPermissions"},
		{Name: "gone", Prompt: "最初の指示", Permission: "bypassPermissions"},
	}}}
	states := map[string]affState{"x": {Sessions: map[string]sessionState{
		"keep": {SessionID: "s-keep"}, "auto": {SessionID: "s-auto"}, "gone": {SessionID: "s-gone"},
	}}}
	got := map[string]action{}
	for _, a := range plan(affs, states, nil) {
		got[a.Name] = a
	}
	if a := got["keep"]; a.Verb != "起こし直す" || a.Refit || strings.Join(a.Args, " ") != "--bg --resume s-keep" {
		t.Fatalf("覚えているならフラグ無しのはず（対照）: %+v", a)
	}
	for _, name := range []string{"auto", "gone"} {
		a := got[name]
		args := strings.Join(a.Args, " ")
		if a.Verb != "起こし直す" || !a.Refit {
			t.Fatalf("%s: 起こし直す（付け直し）のはず: %+v", name, a)
		}
		for _, want := range []string{"-n " + name, `"TSUMUGI_SESSION":"` + name + `"`, "--permission-mode bypassPermissions", "--resume s-" + name, "tsumugi yui"} {
			if !strings.Contains(args, want) {
				t.Fatalf("%s: %q が無い: %s", name, want, args)
			}
		}
		if strings.Contains(args, "最初の指示") {
			t.Fatalf("%s: 起こし直しに最初の指示を付けた: %s", name, args)
		}
		// 指示が無いと、フラグ付きの --resume は会話を読み込まず空の会話で起きる。最後の引数は付け直しの知らせ
		if a.Args[len(a.Args)-1] != refitNotice {
			t.Fatalf("%s: 付け直しの知らせを最後に付けていない: %s", name, args)
		}
	}
}

// 定時・届けで止まっているセッションへ渡すときも、Claude Code が設定を覚えていなければ紡のフラグを付け直す。
// （run の「起こし直す」より先にこの道を通るので、ここが付け直さないと抜けたまま起き続ける）
func TestDeliverPromptRefit(t *testing.T) {
	onDemandSetup(t)
	stubJobRecords(t, map[string]jobRecord{"s-keep": {Name: "keep", NameSource: "user"}})
	af := affiliation{ID: "x"}
	for _, c := range []struct {
		name, id string
		refit    bool
	}{{"keep", "s-keep", false}, {"auto", "s-auto", true}} {
		f := &fakeClaude{}
		f.install(t)
		spec := sessionSpec{Name: c.name, Prompt: "最初の指示", Permission: "bypassPermissions"}
		d := delivery{Affiliation: "x", Name: c.name, SessionID: c.id, Prompt: "【定時】朝の巡回", af: af, spec: spec}
		var out bytes.Buffer
		if ok, _ := deliverPrompt(&out, d); !ok {
			t.Fatalf("%s: 渡していない: %s", c.name, out.String())
		}
		bg := f.bg()
		if len(bg) != 1 {
			t.Fatalf("%s: 1 回起こすはず: %q", c.name, bg)
		}
		args := strings.Join(bg[0], " ")
		if !c.refit {
			if args != "--bg --resume s-keep 【定時】朝の巡回" {
				t.Fatalf("覚えているならフラグ無しのはず（対照）: %s", args)
			}
			continue
		}
		for _, want := range []string{"-n auto", `"TSUMUGI_SESSION":"auto"`, "--permission-mode bypassPermissions", "--resume s-auto"} {
			if !strings.Contains(args, want) {
				t.Fatalf("付け直しに %q が無い: %s", want, args)
			}
		}
		if !strings.HasSuffix(args, "【定時】朝の巡回") || strings.Contains(args, "最初の指示") {
			t.Fatalf("渡すのは定時の指示だけのはず: %s", args)
		}
	}
}

// 動いているのに Claude Code 側の名前が付け替わったものだけを★で知らせる。
func TestWarnLostSettings(t *testing.T) {
	stubJobRecords(t, map[string]jobRecord{
		"s-ok":   {Name: "ok", NameSource: "user"},
		"s-auto": {Name: "lw-monitoring-patrol", NameSource: "auto"},
	})
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{{Name: "ok"}, {Name: "auto"}, {Name: "stopped"}}}}
	states := map[string]affState{"x": {Sessions: map[string]sessionState{
		"ok": {SessionID: "s-ok"}, "auto": {SessionID: "s-auto"}, "stopped": {SessionID: "s-stopped"},
	}}}
	all := []agent{
		{Name: "ok", SessionID: "s-ok", ID: "s-ok", Pid: 1},
		{Name: "lw-monitoring-patrol", SessionID: "s-auto", ID: "b1049fe0", Pid: 2},
	}
	var out bytes.Buffer
	warnLostSettings(&out, "m", affs, states, all)
	got := out.String()
	if strings.Count(got, "★") != 1 || !strings.Contains(got, "x/auto") || !strings.Contains(got, "lw-monitoring-patrol") || !strings.Contains(got, "claude stop b1049fe0") {
		t.Fatalf("知らせ方が違う: %q", got)
	}
}

// 本物の読み方: <CLAUDE_CONFIG_DIR>/jobs/<先頭 8 字>/state.json を読む。
func TestReadJobRecordFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	os.MkdirAll(filepath.Join(dir, "jobs", "b6085668"), 0o700)
	os.WriteFile(filepath.Join(dir, "jobs", "b6085668", "state.json"), []byte(`{"name":"useai-main","nameSource":"user","respawnFlags":["-n","useai-main"]}`), 0o600)
	if !jobKeepsSettings("b6085668-613d-4892-b408-ac7d7b29f268", "useai-main") {
		t.Fatal("記録を読めていない")
	}
	if jobKeepsSettings("b6085668-613d-4892-b408-ac7d7b29f268", "別の名前") || jobKeepsSettings("00000000-0000", "useai-main") {
		t.Fatal("違う名前・無い記録を「覚えている」とした")
	}
}
