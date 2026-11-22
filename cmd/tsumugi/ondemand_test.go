package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func boolp(b bool) *bool { return &b }

// claude を呼ばずに、呼ばれた引数を数える。起こした（--bg）あとは、その名前で動いているように見せる。
type fakeClaude struct {
	calls   [][]string
	started []agent
}

func (f *fakeClaude) install(t *testing.T) {
	old := runClaudeIn
	t.Cleanup(func() { runClaudeIn = old })
	runClaudeIn = func(d string, args ...string) ([]byte, error) {
		f.calls = append(f.calls, args)
		if len(args) > 0 && args[0] == "agents" {
			b, _ := json.Marshal(f.started)
			return b, nil
		}
		if len(args) > 0 && args[0] == "--bg" {
			for i, a := range args {
				if a == "-n" && i+1 < len(args) {
					f.started = append(f.started, agent{Name: args[i+1], SessionID: "new-" + args[i+1], ID: "i", Pid: 9, StartedAt: 1, Status: "idle"})
				}
			}
		}
		return nil, nil
	}
}

func (f *fakeClaude) bg() [][]string {
	var out [][]string
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == "--bg" {
			out = append(out, c)
		}
	}
	return out
}

func onDemandSetup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TSUMUGI_HOME", dir)
	t.Setenv("TSUMUGI_MACHINE", "m")
	resumeWait, stopSettle, settleWait = 0, 0, 0
	t.Cleanup(func() { resumeWait, stopSettle, settleWait = 15*time.Second, 3*time.Second, 30*time.Second })
	resumed, startedNow = map[string]bool{}, map[string]bool{}
	return dir
}

// 常駐しない＋止まっている（一度起こしたもの・まだ一度も起こしていないもの）→ plan は何もしない。
// 常駐（既定・true）は今までどおり起こす・起こし直す（対照）。
func TestPlanOnDemand(t *testing.T) {
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{
		{Name: "od-old", Resident: boolp(false)},
		{Name: "od-new", Resident: boolp(false), Prompt: "待機"},
		{Name: "res-old"},
		{Name: "res-new", Prompt: "待機"},
		{Name: "res-true", Resident: boolp(true)},
	}}}
	states := map[string]affState{"x": {Sessions: map[string]sessionState{
		"od-old": {SessionID: "s1"}, "res-old": {SessionID: "s2"}, "res-true": {SessionID: "s3"},
	}}}
	all := []agent{{Name: "od-old", SessionID: "s1"}, {Name: "res-old", SessionID: "s2"}, {Name: "res-true", SessionID: "s3"}} // どれも止まっている
	got := map[string]string{}
	for _, a := range planOn("m", affs, states, all) {
		got[a.Name] = a.Verb
	}
	want := map[string]string{"res-old": "起こし直す", "res-new": "起こす", "res-true": "起こし直す"}
	if len(got) != len(want) {
		t.Fatalf("やることが違う: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: %q のはず（%v）", k, v, got)
		}
	}
	// tsumugi a・名前を変える（onlySession）は、常駐しないものも起こす
	acts := planOn("m", []affiliation{onlySession(affs[0], affs[0].Sessions[0])}, states, all)
	if len(acts) != 1 || acts[0].Verb != "起こし直す" {
		t.Fatalf("tsumugi a では起こすはず: %+v", acts)
	}
}

// 定時: 常駐しないものも、止まっていれば同じ会話のまま指示を付けて起こす。一度も起こしていなければ、
// 最初の指示と定時の両方を付けて最初の起動をする。常駐で一度も起こしていないものは、今までどおり run の最初の起動に任せる。
func TestScheduleOnDemand(t *testing.T) {
	onDemandSetup(t)
	now := time.Date(2026, 11, 22, 9, 0, 0, 0, time.Local)
	sc := []scheduleSpec{{Cron: "0 9 * * *", Prompt: "朝の巡回"}}
	dir := t.TempDir()
	af := affiliation{ID: "x", Path: filepath.Join(t.TempDir(), "x", configFile), Sessions: []sessionSpec{
		{Name: "od-old", Dir: dir, Resident: boolp(false), Schedules: sc},
		{Name: "od-new", Dir: dir, Resident: boolp(false), Prompt: "最初の指示", Schedules: sc},
		{Name: "res-new", Dir: dir, Prompt: "最初の指示", Schedules: sc},
	}}
	states := map[string]affState{"x": {Sessions: map[string]sessionState{"od-old": {SessionID: "s1"}}}}
	ds := dueDeliveries("m", []affiliation{af}, states, nil, now)
	names := map[string]delivery{}
	for _, d := range ds {
		names[d.Name] = d
	}
	if len(ds) != 2 || names["od-old"].SessionID != "s1" || names["od-new"].SessionID != "" {
		t.Fatalf("届けるものが違う（常駐で一度も起こしていないものは含めない）: %+v", ds)
	}
	f := &fakeClaude{}
	f.install(t)
	var out bytes.Buffer
	if ok, err := deliverSchedules(&out, []affiliation{af}, states, nil, now, false); err != nil || !ok {
		t.Fatalf("渡していない: %v %v %s", ok, err, out.String())
	}
	bg := f.bg()
	if len(bg) != 2 {
		t.Fatalf("2 つ起こすはず: %q", bg)
	}
	var resume, fresh []string
	for _, c := range bg {
		if c[1] == "--resume" {
			resume = c
		} else {
			fresh = c
		}
	}
	if resume == nil || resume[2] != "s1" || !strings.Contains(resume[3], "朝の巡回") {
		t.Fatalf("止まっているものは同じ会話のまま指示を付けて起こすはず: %q", resume)
	}
	last := fresh[len(fresh)-1]
	if !strings.Contains(strings.Join(fresh, " "), "-n od-new") || !strings.HasPrefix(last, "最初の指示") || !strings.Contains(last, "【定時】朝の巡回") {
		t.Fatalf("一度も起こしていないものは、最初の指示と定時の両方を付けて起こすはず: %q", fresh)
	}
	ss, known := states["x"].Sessions["od-new"]
	if !known || ss.Delivered[scheduleKey(sc[0])] == "" {
		t.Fatalf("控えに載り、届けた印が付くはず: %+v", states["x"].Sessions)
	}
	// 同じ発火を二度渡さない
	if ds := dueDeliveries("m", []affiliation{af}, states, nil, now.Add(time.Minute)); len(ds) != 0 {
		t.Fatalf("二度届けている: %+v", ds)
	}
}

// 届け: 定時と同じ。一度も起こしていない常駐しないものは、最初の指示と届けを付けて起こし、会話 ID を控える。
// 常駐で一度も起こしていないものは、今までどおり次の回に（対照）。
func TestDeliverQueueOnDemand(t *testing.T) {
	root := onDemandSetup(t)
	resumeWait = 10 * time.Millisecond // 起こした会話の ID を一覧から拾わせる
	dir := t.TempDir()
	af := affiliation{ID: "x", Path: filepath.Join(root, "x", configFile), Sessions: []sessionSpec{
		{Name: "od-new", Dir: dir, Resident: boolp(false), Prompt: "最初の指示"},
		{Name: "od-old", Dir: dir, Resident: boolp(false)},
		{Name: "res-new", Dir: dir, Prompt: "最初の指示"},
	}}
	now := time.Now()
	for _, n := range []string{"od-new", "od-old", "res-new"} {
		if _, err := enqueue(af, n, "用事", now); err != nil {
			t.Fatal(err)
		}
	}
	states := map[string]affState{"x": {Sessions: map[string]sessionState{"od-old": {SessionID: "s1"}}}}
	f := &fakeClaude{}
	f.install(t)
	var out bytes.Buffer
	if ok, err := deliverQueue(&out, []affiliation{af}, states, nil, now, false); err != nil || !ok {
		t.Fatalf("渡していない: %v %v %s", ok, err, out.String())
	}
	bg := f.bg()
	if len(bg) != 2 {
		t.Fatalf("常駐しない 2 つだけを起こすはず: %q\n%s", bg, out.String())
	}
	for _, c := range bg {
		j := strings.Join(c, " ")
		switch {
		case strings.Contains(j, "--resume s1"):
			if !strings.Contains(c[len(c)-1], "用事") {
				t.Fatalf("届けが付いていない: %q", c)
			}
		case strings.Contains(j, "-n od-new"):
			if last := c[len(c)-1]; !strings.HasPrefix(last, "最初の指示") || !strings.Contains(last, "用事") {
				t.Fatalf("最初の指示と届けの両方を付けるはず: %q", c)
			}
		default:
			t.Fatalf("起こすはずのないものを起こした: %q", c)
		}
	}
	if id := states["x"].Sessions["od-new"].SessionID; id != "new-od-new" {
		t.Fatalf("起こした会話の ID を控えるはず: %q", id)
	}
	st, _ := loadState(af)
	if st.Sessions["od-new"].SessionID != "new-od-new" {
		t.Fatalf("状態.json に書くはず: %+v", st)
	}
	qs, _ := loadQueue(af)
	if len(qs) != 1 || qs[0].Name != "res-new" {
		t.Fatalf("常駐で一度も起こしていないものだけが残るはず: %+v", qs)
	}
	if !strings.Contains(out.String(), "res-new: 届け 1 件 … まだ一度も起こしていないので次の回に") {
		t.Fatalf("常駐は今までどおり次の回に: %s", out.String())
	}
}

// 一覧と status: 常駐しないものが止まっていても「★」を付けず、必要なときに起こすと分かる表示にする。
// 見張り: 止まっているものは知らせない。
func TestListAndWatchOnDemand(t *testing.T) {
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{
		{Name: "od", Resident: boolp(false)},
		{Name: "res"},
	}}}
	var b bytes.Buffer
	printListOn(&b, "m", affs, nil)
	lines := strings.Split(b.String(), "\n")
	if !strings.Contains(lines[1], "止まっている（必要なときに起こす）") || strings.Contains(lines[2], "必要なとき") || !strings.Contains(lines[2], "止まっている") {
		t.Fatalf("一覧の表示が違う:\n%s", b.String())
	}
	b.Reset()
	printStatus(&b, compare(affs, nil))
	lines = strings.Split(b.String(), "\n")
	if !strings.Contains(lines[1], "止まっている（必要なときに起こす）") || strings.Contains(lines[1], "★") || !strings.Contains(lines[2], "★動いていない") {
		t.Fatalf("status の表示が違う:\n%s", b.String())
	}
	t.Setenv("TSUMUGI_MACHINE", "m")
	states := map[string]affState{"x": {Sessions: map[string]sessionState{"od": {SessionID: "s1", Watch: &watchState{State: "idle", Since: "2026-11-01T00:00:00+09:00"}}}}}
	b.Reset()
	affs[0].Watch.Notify = []string{"false"}
	watch(&b, affs, states, []agent{{Name: "od", SessionID: "s1"}}, time.Now(), false)
	if b.Len() != 0 || states["x"].Sessions["od"].Watch != nil {
		t.Fatalf("止まっている常駐しないものを知らせた: %q %+v", b.String(), states["x"].Sessions["od"].Watch)
	}
}
