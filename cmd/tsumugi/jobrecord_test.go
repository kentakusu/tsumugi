package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 起こし直す: Claude Code が紡の名前を覚えていれば今までどおりフラグ無し。
// 記録が無い・名前が自動で付け替わったときは、紡のフラグを全部付けて --resume する（指示は付けない）。
func TestPlanRefitWhenJobForgot(t *testing.T) {
	stubJobRecords(t, map[string]jobRecord{
		"s-keep": {Name: "keep", NameSource: "user", State: "stopped", RespawnFlags: []string{"-n"}},
		"s-auto": {Name: "cc-34-migration-handoff", NameSource: "auto"},
		// 名前は残っているが failed（2026-11-22 UseAI の useai-accounting）・フラグが空
		"s-failed":  {Name: "failed", NameSource: "user", State: "failed", RespawnFlags: []string{"-n"}},
		"s-noflags": {Name: "noflags", NameSource: "user", State: "stopped"},
	})
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{
		{Name: "keep", Prompt: "最初の指示", Permission: "bypassPermissions"},
		{Name: "auto", Prompt: "最初の指示", Permission: "bypassPermissions"},
		{Name: "gone", Prompt: "最初の指示", Permission: "bypassPermissions"},
		{Name: "failed", Prompt: "最初の指示", Permission: "bypassPermissions"},
		{Name: "noflags", Prompt: "最初の指示", Permission: "bypassPermissions"},
	}}}
	states := map[string]affState{"x": {Sessions: map[string]sessionState{
		"keep": {SessionID: "s-keep"}, "auto": {SessionID: "s-auto"}, "gone": {SessionID: "s-gone"},
		"failed": {SessionID: "s-failed"}, "noflags": {SessionID: "s-noflags"},
	}}}
	got := map[string]action{}
	for _, a := range plan(affs, states, nil) {
		got[a.Name] = a
	}
	if a := got["keep"]; a.Verb != "起こし直す" || a.Refit || strings.Join(a.Args, " ") != "--bg --resume s-keep" {
		t.Fatalf("覚えているならフラグ無しのはず（対照）: %+v", a)
	}
	for _, name := range []string{"auto", "gone", "failed", "noflags"} {
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
	stubJobRecords(t, map[string]jobRecord{
		"s-keep": {Name: "keep", NameSource: "user", State: "stopped", RespawnFlags: []string{"-n"}},
		// 名前もフラグも残っているが failed（2026-11-22 UseAI: フラグ無しで渡すと auto の名前・フラグ無しの写しになった）
		"s-failed": {Name: "failed", NameSource: "user", State: "failed", RespawnFlags: []string{"-n"}},
	})
	af := affiliation{ID: "x"}
	for _, c := range []struct {
		name, id string
		refit    bool
	}{{"keep", "s-keep", false}, {"auto", "s-auto", true}, {"failed", "s-failed", true}} {
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
		for _, want := range []string{"-n " + c.name, `"TSUMUGI_SESSION":"` + c.name + `"`, "--permission-mode bypassPermissions", "--resume " + c.id} {
			if !strings.Contains(args, want) {
				t.Fatalf("付け直しに %q が無い: %s", want, args)
			}
		}
		if !strings.HasSuffix(args, "【定時】朝の巡回") || strings.Contains(args, "最初の指示") {
			t.Fatalf("渡すのは定時の指示だけのはず: %s", args)
		}
	}
}

// 会話の中身がある会話 ID を差し替える（ほかは「中身が無い」）。
func stubTranscripts(t *testing.T, ids ...string) {
	t.Helper()
	old := hasTranscript
	t.Cleanup(func() { hasTranscript = old })
	have := map[string]bool{}
	for _, id := range ids {
		have[id] = true
	}
	hasTranscript = func(id string) bool { return have[id] }
}

// record は、控えの会話から中身の無い会話へは乗り換えない（★で知らせる）。初めて控えるもの・中身のある写しは控える。
func TestRecordSkipsEmptyConversation(t *testing.T) {
	stubTranscripts(t, "REAL", "COPY")
	dir := t.TempDir()
	af := affiliation{ID: "a", Path: filepath.Join(dir, "a", configFile), Sessions: []sessionSpec{{Name: "acc"}, {Name: "oss"}, {Name: "new"}}}
	os.MkdirAll(filepath.Join(dir, "a"), 0o700)
	states := map[string]affState{"a": {Sessions: map[string]sessionState{
		"acc": {SessionID: "REAL"}, "oss": {SessionID: "OLD"},
	}}}
	all := []agent{
		{Name: "acc", SessionID: "EMPTY", ID: "c6e57e3e", Pid: 1, StartedAt: 1}, // 一覧から開いて起きた空の会話
		{Name: "oss", SessionID: "COPY", ID: "copy", Pid: 2, StartedAt: 1},      // 中身のある写し（対照）
		{Name: "new", SessionID: "FRESH", ID: "fresh", Pid: 3, StartedAt: 1},    // この run で初めて起こしたもの（対照）
	}
	learnIDs(states)
	startedNow = map[string]bool{"new": true}
	defer func() { startedNow = map[string]bool{}; knownIDs = map[string]string{} }()
	var out bytes.Buffer
	if err := record(&out, []affiliation{af}, states, all); err != nil {
		t.Fatal(err)
	}
	if got := states["a"].Sessions["acc"].SessionID; got != "REAL" {
		t.Fatalf("中身の無い会話に乗り換えた: %s", got)
	}
	if !strings.Contains(out.String(), "a/acc") || !strings.Contains(out.String(), "claude stop c6e57e3e") || strings.Count(out.String(), "★") != 1 {
		t.Fatalf("知らせ方が違う: %q", out.String())
	}
	if got := states["a"].Sessions["oss"].SessionID; got != "COPY" {
		t.Fatalf("中身のある写しに乗り換えていない: %s", got)
	}
	if got := states["a"].Sessions["new"].SessionID; got != "FRESH" {
		t.Fatalf("初めて起こしたものを控えていない: %s", got)
	}
}

// adoptCopy は中身の無い会話を写しとして引き取らない。
func TestAdoptCopySkipsEmpty(t *testing.T) {
	stubTranscripts(t)
	old := runClaudeIn
	defer func() { runClaudeIn = old; resumeWait = 15 * time.Second; knownIDs = map[string]string{} }()
	resumeWait = 0
	t0 := time.Now()
	runClaudeIn = func(d string, args ...string) ([]byte, error) {
		return []byte(fmt.Sprintf(`[{"name":"useai-accounting","sessionId":"EMPTY","pid":7,"cwd":"/w","startedAt":%d}]`, t0.Add(time.Second).UnixMilli())), nil
	}
	if id := adoptCopy(delivery{Name: "s", Dir: "/w", SessionID: "OLD"}, t0); id != "" {
		t.Fatalf("中身の無い会話を引き取った: %q", id)
	}
}

// 本物の探し方: projects の下のどの道にあっても見つける（worktree に入った会話）。中身の無い記録は数えない。
func TestHasTranscriptFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	wt := filepath.Join(dir, "projects", "-home-x-oss-tsumugi--claude-worktrees-on-demand")
	os.MkdirAll(wt, 0o700)
	os.WriteFile(filepath.Join(wt, "6e850902.jsonl"), []byte(`{"type":"queue-operation"}`+"\n"+`{"type":"user","message":{"content":"hi"}}`+"\n"), 0o600)
	os.WriteFile(filepath.Join(wt, "aaaa.jsonl"), []byte(`{"type":"queue-operation"}`+"\n"), 0o600)
	if !hasTranscript("6e850902") {
		t.Fatal("worktree の道にある会話を見つけない")
	}
	if hasTranscript("aaaa") || hasTranscript("none") || hasTranscript("") {
		t.Fatal("中身の無い・無い会話を「中身がある」とした")
	}
}

// daemon.log の最後の「daemon start」の時刻を読む。
func TestReadDaemonStartedAt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if _, ok := readDaemonStartedAt(); ok {
		t.Fatal("daemon.log が無いのに時刻を返した")
	}
	log := "[2026-11-22T08:30:47.254Z] [supervisor] ─── daemon start ─── version=2.1.330 pid=1 origin=transient\n" +
		"[2026-11-22T08:30:47.389Z] [bg] bg claimed-spare b80362be (shell)\n" +
		"[2026-11-22T08:55:01.456Z] [supervisor] ─── daemon start ─── version=2.1.330 pid=2 origin=transient\n" +
		"[2026-11-22T08:55:02.137Z] [bg] bg adopt: adopted=0 respawned=0 dead=1\n"
	os.WriteFile(filepath.Join(dir, "daemon.log"), []byte(log), 0o600)
	got, ok := readDaemonStartedAt()
	want := time.Date(2026, 11, 22, 8, 55, 1, 456000000, time.UTC)
	if !ok || !got.Equal(want) {
		t.Fatalf("最後の起動時刻でない: %v %v", got, ok)
	}
}

// daemon が立ち上がってから daemonSettle の間は、起こし直しも、止まっているものへの定時・届けもしない（次の回に）。
// 過ぎていれば今までどおり（対照）。
func TestWaitAfterDaemonStart(t *testing.T) {
	onDemandSetup(t)
	stubJobRecords(t, map[string]jobRecord{"s1": {Name: "s", NameSource: "user", State: "stopped", RespawnFlags: []string{"-n"}}})
	stubTranscripts(t, "s1")
	old := daemonStartedAt
	t.Cleanup(func() { daemonStartedAt = old })
	for _, c := range []struct {
		ago  time.Duration
		wait bool
	}{{10 * time.Second, true}, {5 * time.Minute, false}} {
		daemonStartedAt = func() (time.Time, bool) { return time.Now().Add(-c.ago), true }
		if got := daemonJustStarted(time.Now()) > 0; got != c.wait {
			t.Fatalf("%v 前に立ち上がった: 待つ=%v のはず", c.ago, c.wait)
		}
		f := &fakeClaude{}
		f.install(t)
		var out bytes.Buffer
		ok, _ := deliverPrompt(&out, delivery{Affiliation: "x", Name: "s", SessionID: "s1", Prompt: "【定時】x"})
		if c.wait && (ok || len(f.bg()) != 0 || !strings.Contains(out.String(), "立ち上がったばかり")) {
			t.Fatalf("立ち上がった直後に止まっているものを起こした: %v %q %s", ok, f.bg(), out.String())
		}
		if !c.wait && (!ok || len(f.bg()) != 1) {
			t.Fatalf("落ち着いたあとは渡すはず（対照）: %v %q %s", ok, f.bg(), out.String())
		}
	}
}

// 動いているのに Claude Code 側の名前が付け替わったものだけを★で知らせる。
func TestWarnLostSettings(t *testing.T) {
	stubJobRecords(t, map[string]jobRecord{
		"s-ok":   {Name: "ok", NameSource: "user", State: "stopped", RespawnFlags: []string{"-n"}},
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
