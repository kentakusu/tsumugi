package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	if a := got["keep"]; a.Verb != "起こし直す" || a.Refit || strings.Join(a.Args, " ") != "respawn s-keep" {
		t.Fatalf("覚えているなら claude respawn のはず（対照）: %+v", a)
	}
	// failed でも名前とフラグが残っていれば claude respawn（--resume の道は予備のプロセスで写しになるため）
	if a := got["failed"]; a.Verb != "起こし直す" || a.Refit || strings.Join(a.Args, " ") != "respawn s-failed" {
		t.Fatalf("failed でも設定が残っていれば claude respawn のはず: %+v", a)
	}
	for _, name := range []string{"auto", "gone", "noflags"} {
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
	}{{"keep", "s-keep", false}, {"auto", "s-auto", true}, {"failed", "s-failed", false}} {
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
			if args != "--bg --resume "+c.id+" 【定時】朝の巡回" {
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
	af := affiliation{ID: "a", Path: filepath.Join(dir, "a", configFile), Sessions: []sessionSpec{{Name: "acc"}, {Name: "oss"}, {Name: "new"}, {Name: "main"}}}
	os.MkdirAll(filepath.Join(dir, "a"), 0o700)
	states := map[string]affState{"a": {Sessions: map[string]sessionState{
		"acc": {SessionID: "REAL"}, "oss": {SessionID: "OLD"}, "main": {SessionID: "BEFORE"},
	}}}
	all := []agent{
		{Name: "acc", SessionID: "EMPTY", ID: "c6e57e3e", Pid: 1, StartedAt: 1}, // 一覧から開いて起きた空の会話
		{Name: "oss", SessionID: "COPY", ID: "copy", Pid: 2, StartedAt: 1},      // 中身のある写し（対照）
		{Name: "new", SessionID: "FRESH", ID: "fresh", Pid: 3, StartedAt: 1},    // この run で初めて起こしたもの（対照）
		// 起きたばかりで、まだ会話の記録を書いていない付け直しの写し（2026-11-22 Reatex の 73e96e35）
		{Name: "main", SessionID: "JUST", ID: "73e96e35", Pid: 4, StartedAt: time.Now().Add(-3 * time.Second).UnixMilli()},
	}
	learnIDs(states)
	startedNow = map[string]bool{"new": true}
	defer func() { startedNow = map[string]bool{}; knownIDs = map[string]string{} }()
	f := &fakeClaude{}
	f.install(t)
	var out bytes.Buffer
	if err := record(&out, []affiliation{af}, states, all); err != nil {
		t.Fatal(err)
	}
	// 乗り換えた oss の古い ID（OLD）だけを claude rm で一覧から外す。乗り換えなかった acc・初めての new には触らない
	var rms []string
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == "rm" {
			rms = append(rms, strings.Join(c, " "))
		}
	}
	if len(rms) != 1 || rms[0] != "rm OLD" {
		t.Fatalf("古い ID の片付けが違う: %q", rms)
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
	// 起きたばかりのものは判定を次の回に回す: まだ乗り換えず、★も出さない（★は acc の 1 つだけ・rm も OLD だけ＝上で確かめた）
	if got := states["a"].Sessions["main"].SessionID; got != "BEFORE" || strings.Contains(out.String(), "73e96e35") {
		t.Fatalf("起きたばかりの写しを判定した: %s %q", got, out.String())
	}
	// 次の回: 記録が書かれていれば乗り換え、古い ID を外す
	stubTranscripts(t, "REAL", "COPY", "JUST")
	f2 := &fakeClaude{}
	f2.install(t)
	out.Reset()
	record(&out, []affiliation{af}, states, all)
	if got := states["a"].Sessions["main"].SessionID; got != "JUST" {
		t.Fatalf("記録が書かれたあとも乗り換えていない: %s", got)
	}
	if len(f2.calls) != 1 || strings.Join(f2.calls[0], " ") != "rm BEFORE" {
		t.Fatalf("乗り換えたあと古い ID を外していない: %q", f2.calls)
	}
}

// 古い ID が動いているときは claude rm しない。
func TestRetireOldIDSkipsRunning(t *testing.T) {
	f := &fakeClaude{}
	f.install(t)
	var out bytes.Buffer
	retireOldID(&out, "OLD", []agent{{SessionID: "OLD", Pid: 5}})
	retireOldID(&out, "", nil)
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == "rm" {
			t.Fatalf("動いているもの・空の ID を claude rm した: %q", f.calls)
		}
	}
	retireOldID(&out, "c7f68470-e565-4ded-af40-7d658b222bdf", nil)
	if len(f.calls) != 1 || strings.Join(f.calls[0], " ") != "rm c7f68470" {
		t.Fatalf("止まっている古い ID を短い ID で外していない（対照）: %q", f.calls)
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

// チャネルが使えるとき、止まっている会話（設定は覚えている）への定時・届けは、claude respawn で同じ ID のまま起こし、
// チャネルで渡す。--resume（予備のプロセスを使うと写しになる）は打たない。
func TestDeliverRespawnsAndUsesChannel(t *testing.T) {
	dir, err := os.MkdirTemp(sockTempBase(), "tsrs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("TSUMUGI_HOME", dir)
	os.WriteFile(filepath.Join(dir, globalFile), []byte(`{"チャネル":true}`), 0o600)
	old := respawnWait
	respawnWait = 5 * time.Second
	t.Cleanup(func() { respawnWait = old })
	stubJobRecords(t, map[string]jobRecord{"S1": {Name: "s", NameSource: "user", State: "failed", RespawnFlags: []string{"--channels", "-n"}}})
	var calls [][]string
	running := false
	oldRun := runClaudeIn
	t.Cleanup(func() { runClaudeIn = oldRun })
	got := make(chan map[string]any, 1)
	runClaudeIn = func(d string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		switch args[0] {
		case "respawn":
			running = true
			go channelListen("a", "s", func(v any) { got <- v.(map[string]any) }) // 起きたセッションのチャネル
		case "agents":
			if running {
				return []byte(`[{"name":"s","sessionId":"S1","id":"S1","pid":9,"status":"idle"}]`), nil
			}
			return []byte("[]"), nil
		}
		return nil, nil
	}
	var out bytes.Buffer
	ok, newID := deliverPrompt(&out, delivery{Affiliation: "a", Name: "s", SessionID: "S1", Prompt: "【定時】朝の巡回", spec: sessionSpec{Name: "s"}})
	for _, c := range calls {
		if c[0] == "--bg" {
			t.Fatalf("--resume を打った（写しになりうる）: %q", calls)
		}
	}
	if len(calls) == 0 || calls[0][0] != "respawn" || calls[0][1] != "S1" {
		t.Fatalf("claude respawn で起こしていない: %q", calls)
	}
	if !ok || newID != "" {
		t.Fatalf("渡していない・ID が変わった: %v %q %s", ok, newID, out.String())
	}
	select {
	case m := <-got:
		if !strings.Contains(fmt.Sprint(m["params"]), "【定時】朝の巡回") {
			t.Fatalf("チャネルで渡したものが違う: %v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("チャネルに届いていない")
	}
}

// 錠の持ち主が落ちたか（Linux は /proc/<pid>/cmdline で見る）。
func TestLockHolderAlive(t *testing.T) {
	oh := holderProcess
	t.Cleanup(func() { holderProcess = oh })
	holderProcess = func(pid int) (bool, string, bool) {
		switch pid {
		case 400:
			return true, `C:\Users\u\.local\bin\tsumugi.exe`, true
		case 500:
			return true, "bash", true
		case 600:
			return false, "", true
		}
		return false, "", false
	}
	dir := t.TempDir()
	old := procCmdline
	t.Cleanup(func() { procCmdline = old })
	procCmdline = func(pid int) string { return filepath.Join(dir, fmt.Sprint(pid)) }
	os.WriteFile(filepath.Join(dir, "100"), []byte("/home/u/.local/bin/tsumugi\x00run\x00"), 0o600)
	os.WriteFile(filepath.Join(dir, "200"), []byte("/usr/bin/bash\x00"), 0o600)
	for _, c := range []struct {
		goos, content string
		alive         bool
	}{
		{"linux", "100 2026-11-22T19:19:27+09:00", true},  // 紡として動いている
		{"linux", "200 2026-11-22T19:19:27+09:00", false}, // pid が別のプロセスに使い回された
		{"linux", "300 2026-11-22T19:19:27+09:00", false}, // プロセスが無い（WSL を起こし直したあと）
		{"linux", "", true}, // 読めない中身は分からない＝動いているとみなす
		// macOS・Windows はプロセスの見方（holderProcess）で見る
		{"windows", "400 x", true},  // 紡として動いている
		{"darwin", "500 x", false},  // pid が別のプロセスに使い回された
		{"windows", "600 x", false}, // プロセスが無い
		{"darwin", "700 x", true},   // 見えない（権限など）＝動いているとみなす
	} {
		if got := lockHolderAliveOn(c.goos, c.content); got != c.alive {
			t.Fatalf("%s %q: %v のはず", c.goos, c.content, c.alive)
		}
	}
}

// 持ち主が落ちた錠は、印が新しくてもすぐ奪う（Linux のときだけ。ここではプロセスの見方を差し替えて試す）。
func TestLockStealsDeadHolder(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux の /proc で見る分岐（ほかの土台は TestLockHolderAlive で見る）")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, lockFile), []byte("999999 2026-11-22T19:19:27+09:00\n"), 0o600)
	unlock, err := lockHome(dir, 0)
	if err != nil {
		t.Fatalf("落ちた持ち主の錠を奪わない: %v", err)
	}
	unlock()
}

// 紡の設定を持つ job の記録（--settings に紡の env が入っている）。
func tsumugiJob(aff, name string) jobRecord {
	return jobRecord{Name: name, NameSource: "user", State: "working", RespawnFlags: []string{
		"-n", name, "--settings", `{"autoMemoryEnabled":false,"env":{"TSUMUGI_AFFILIATION":"` + aff + `","TSUMUGI_SESSION":"` + name + `"}}`}}
}

// 動いているものを配下に揃える:
//   - 控えの会話が紡の設定を持っていれば何もしない（対照）
//   - 控えの会話に紡の設定が無ければ、空いているとき同じ ID のまま付け直す（stop → rm → フラグ付き --resume ＋知らせ）
//   - 作業中なら次の回に回す
//   - 手で起こした同じ名前のもの: 紡の設定が無ければ付け直して控える／あればそのまま控える
func TestAdoptRunning(t *testing.T) {
	onDemandSetup(t)
	stubJobRecords(t, map[string]jobRecord{
		"s-ok":    tsumugiJob("x", "ok"),
		"s-auto":  {Name: "lw-monitoring-patrol", NameSource: "auto", State: "working"},
		"s-busy":  {Name: "busy-auto", NameSource: "auto", State: "working"},
		"s-hand":  {Name: "hand", NameSource: "user", State: "working", RespawnFlags: []string{"-n", "hand"}},
		"s-hand2": tsumugiJob("x", "hand2"),
		"s-talk":  {Name: "talk-auto", NameSource: "auto", State: "working"},
	})
	// 手で起こしたものは会話の中身がある。s-empty・s-fresh は中身の無い空の会話（再起動直後に Claude Code が起こしたもの）
	stubTranscripts(t, "s-hand", "s-hand2", "s-talk")
	// 最後のやり取りからの時間: s-talk だけ 2 分前（人と話している最中の idle）。ほかは 20 分前
	oldQuiet := quietFor
	t.Cleanup(func() { quietFor = oldQuiet })
	quietFor = func(id string, now time.Time) (time.Duration, bool) {
		if id == "s-talk" {
			return 2 * time.Minute, true
		}
		return 20 * time.Minute, true
	}
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{{Name: "ok"}, {Name: "auto"}, {Name: "busy"}, {Name: "hand"}, {Name: "hand2"}, {Name: "empty"}, {Name: "fresh"}, {Name: "talk"}}}}
	states := map[string]affState{"x": {Sessions: map[string]sessionState{
		"ok": {SessionID: "s-ok"}, "auto": {SessionID: "s-auto"}, "busy": {SessionID: "s-busy"},
		"empty": {SessionID: "s-empty-real"}, "fresh": {SessionID: "s-fresh-real"}, "talk": {SessionID: "s-talk"},
	}}}
	all := []agent{
		{Name: "ok", SessionID: "s-ok", ID: "ok000000", Pid: 1, Status: "idle"},
		{Name: "lw-monitoring-patrol", SessionID: "s-auto", ID: "b1049fe0", Pid: 2, Status: "idle"},
		{Name: "busy-auto", SessionID: "s-busy", ID: "busy0000", Pid: 3, Status: "busy"},
		{Name: "hand", SessionID: "s-hand", ID: "hand0000", Pid: 4, Status: "idle", StartedAt: 5},
		{Name: "hand2", SessionID: "s-hand2", ID: "hand2000", Pid: 5, Status: "busy", StartedAt: 5},
		{Name: "empty", SessionID: "s-empty", ID: "2f3fa587", Pid: 6, Status: "idle", StartedAt: 5},
		{Name: "fresh", SessionID: "s-fresh", ID: "fresh000", Pid: 7, Status: "idle", StartedAt: time.Now().UnixMilli()},
		{Name: "talk-auto", SessionID: "s-talk", ID: "talk0000", Pid: 8, Status: "idle"}, // 2 分前に話していた（付け直さない）
	}
	f := &fakeClaude{}
	f.install(t)
	var out bytes.Buffer
	if !adoptRunning(&out, "m", affs, states, all, false) {
		t.Fatalf("何もしなかった: %s", out.String())
	}
	var calls []string
	for _, c := range f.calls {
		if c[0] == "agents" {
			continue
		}
		s := strings.Join(c, " ")
		if c[0] == "--bg" {
			// 付け直しの起動は、名前・紡の env・同じ ID の --resume・知らせを持つ
			for _, want := range []string{"--resume s-", `"TSUMUGI_AFFILIATION":"x"`, refitNotice} {
				if !strings.Contains(s, want) {
					t.Fatalf("付け直しの起動に %q が無い: %s", want, s)
				}
			}
			for i, a := range c {
				if a == "-n" {
					s = "--bg -n " + c[i+1]
				}
			}
		}
		calls = append(calls, s)
	}
	// 空の会話（古い）は配下に入れず、止めて一覧から外す。起きたばかりの空の会話（fresh）は様子を見る（何もしない）
	want := []string{"stop b1049fe0", "rm s-auto", "--bg -n auto", "stop hand0000", "rm s-hand", "--bg -n hand", "stop 2f3fa587", "rm s-empty"}
	if strings.Join(calls, " | ") != strings.Join(want, " | ") {
		t.Fatalf("呼び方が違う:\n got %q\nwant %q\n%s", calls, want, out.String())
	}
	st := states["x"].Sessions
	if st["auto"].SessionID != "s-auto" || st["hand"].SessionID != "s-hand" || st["hand2"].SessionID != "s-hand2" || st["busy"].SessionID != "s-busy" {
		t.Fatalf("控えが違う（同じ ID のまま控えるはず）: %+v", st)
	}
	if st["empty"].SessionID != "s-empty-real" || st["fresh"].SessionID != "s-fresh-real" {
		t.Fatalf("空の会話を控えに入れた: %+v", st)
	}
	if !strings.Contains(out.String(), "作業中なので次の回に") {
		t.Fatalf("作業中のものを次の回に回していない: %s", out.String())
	}
	if !strings.Contains(out.String(), "x/talk: s-talk に紡の設定が付いていないが、最後のやり取りから 2 分なので、10 分たってから付け直す") {
		t.Fatalf("話している最中のものを待つと知らせていない: %s", out.String())
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
