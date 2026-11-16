package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeAff(t *testing.T, root, id, body string) {
	t.Helper()
	d := filepath.Join(root, id)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, configFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAffiliations(t *testing.T) {
	root := t.TempDir()
	writeAff(t, root, "jisha", `{"表示名":"自社","セッション":[{"名前":"jisha-main","作業フォルダ":"/w/main","最初の指示":"待機"}]}`)
	writeAff(t, root, "kojin", `{"表示名":"個人","セッション":[]}`)
	os.MkdirAll(filepath.Join(root, "所属じゃない"), 0o700) // 所属.json が無いフォルダは数えない

	affs, err := loadAffiliations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(affs) != 2 || affs[0].ID != "jisha" || affs[1].ID != "kojin" {
		t.Fatalf("所属の読み方が違う: %+v", affs)
	}
	if affs[0].Sessions[0].Name != "jisha-main" || affs[0].Label != "自社" {
		t.Fatalf("中身の読み方が違う: %+v", affs[0])
	}
}

func TestLoadAffiliationsMissingRoot(t *testing.T) {
	affs, err := loadAffiliations(filepath.Join(t.TempDir(), "無い"))
	if err != nil || len(affs) != 0 {
		t.Fatalf("置き場所が無いときは 0 件で通すはず: %v %v", affs, err)
	}
}

func TestLoadAffiliationRejects(t *testing.T) {
	cases := map[string]string{
		"知らないキー":   `{"表示名":"x","セッッション":[]}`,
		"名前が無い":    `{"表示名":"x","セッション":[{"作業フォルダ":"/w"}]}`,
		"名前が重複":    `{"表示名":"x","セッション":[{"名前":"a"},{"名前":"a"}]}`,
		"名前が数字だけ":  `{"表示名":"x","セッション":[{"名前":"12"}]}`,
		"JSON でない": `表示名: x`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeAff(t, root, "a", body)
			if _, err := loadAffiliations(root); err == nil {
				t.Fatal("弾くはずが通った")
			}
		})
	}
}

func TestExpandHome(t *testing.T) {
	u, _ := os.UserHomeDir()
	if got := expandHome("~/w"); got != filepath.Join(u, "w") {
		t.Fatalf("~/w → %s", got)
	}
	if got := expandHome("/abs/w"); got != "/abs/w" {
		t.Fatalf("絶対パスは変えないはず: %s", got)
	}
}

// claude agents --json の実物（2.1.32x）の形。
const sampleAgents = `[
 {"pid":5044,"cwd":"/home/u","kind":"interactive","startedAt":1794413017874,"sessionId":"e96a","name":"kenta-8c","status":"busy"},
 {"pid":6001,"cwd":"/w/main","kind":"background","startedAt":1794413017875,"sessionId":"c773","name":"jisha-main","status":"idle"}
]`

func TestCompare(t *testing.T) {
	as, err := parseAgents([]byte(sampleAgents))
	if err != nil {
		t.Fatal(err)
	}
	affs := []affiliation{{ID: "jisha", Sessions: []sessionSpec{
		{Name: "jisha-main", Dir: "/w/main"},
		{Name: "jisha-sub", Dir: "/w/sub"},
	}}}
	rows := compare(affs, as)
	if len(rows) != 3 {
		t.Fatalf("3 行のはず: %+v", rows)
	}
	if rows[0].Name != "jisha-main" || rows[0].State != "idle" || rows[0].Kind != "background" {
		t.Fatalf("動いているものの対応が違う: %+v", rows[0])
	}
	if rows[1].Name != "jisha-sub" || rows[1].State != "" {
		t.Fatalf("動いていないものは State が空のはず: %+v", rows[1])
	}
	if rows[2].Name != "kenta-8c" || rows[2].Affiliation != "" || rows[2].Want {
		t.Fatalf("所属に無いものは最後に（所属なし）で出るはず: %+v", rows[2])
	}

	var buf bytes.Buffer
	printStatus(&buf, rows)
	for _, want := range []string{"所属", "jisha-main", "★動いていない", "（所属なし）"} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("出力に %q が無い:\n%s", want, buf.String())
		}
	}
}

func TestParseAgentsBroken(t *testing.T) {
	if _, err := parseAgents([]byte("not json")); err == nil {
		t.Fatal("壊れた出力を通した")
	}
}

func TestPlan(t *testing.T) {
	all := []agent{
		{Name: "a-run", Pid: 1, SessionID: "s-run", ID: "s-run"},
		{Name: "a-old", SessionID: "s-old"},                    // 止まっている
		{Name: "a-gone", Pid: 2, SessionID: "s-gone", ID: "g"}, // 設定から消えた
		{Name: "hand", Pid: 3, SessionID: "s-hand"},            // 手で開いたもの
	}
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{
		{Name: "a-run"},
		{Name: "a-old"},
		{Name: "a-new", Prompt: "待機", Permission: "dontAsk"},
		{Name: "a-copy", From: "s-hand"},
	}}}
	states := map[string]affState{"x": {Sessions: map[string]sessionState{
		"a-run": {SessionID: "s-run"}, "a-old": {SessionID: "s-old"}, "a-gone": {SessionID: "s-gone"},
	}}}
	acts := plan(affs, states, all)
	got := map[string]action{}
	for _, a := range acts {
		got[a.Name] = a
	}
	if _, ok := got["a-run"]; ok || len(acts) != 4 {
		t.Fatalf("やることが違う: %+v", acts)
	}
	if a := got["a-old"]; a.Verb != "起こし直す" || strings.Join(a.Args, " ") != "--bg --resume s-old" {
		t.Fatalf("起こし直しはフラグ無しの --resume のはず: %+v", a)
	}
	if a := strings.Join(got["a-new"].Args, " "); !strings.Contains(a, "-n a-new") || !strings.Contains(a, `"autoMemoryEnabled":false`) || !strings.Contains(a, `"TSUMUGI_SESSION":"a-new"`) || !strings.Contains(a, "tsumugi yui") ||
		!strings.Contains(a, "--permission-mode dontAsk") || !strings.HasSuffix(a, "待機") {
		t.Fatalf("新しく起こす引数が違う: %s", a)
	}
	if a := got["a-copy"]; a.Verb != "写して起こす" || !strings.HasSuffix(strings.Join(a.Args, " "), "--resume s-hand") {
		t.Fatalf("写しの引数が違う: %+v", a)
	}
	if a := got["a-gone"]; a.Verb != "止める" || strings.Join(a.Args, " ") != "stop g" {
		t.Fatalf("要らなくなったものは止めるはず: %+v", a)
	}
	if _, ok := got["hand"]; ok {
		t.Fatal("手で開いたものに触った")
	}
}

func TestInstallFiles(t *testing.T) {
	e := installEnv{Exe: "/opt/my tools/tsumugi", Path: "/a:/b", LogFile: "/h/.tsumugi/run.log"}
	fs := systemdFiles("/u", e)
	if len(fs) != 2 || !strings.Contains(fs[0].Body, `ExecStart="/opt/my tools/tsumugi" run`) ||
		!strings.Contains(fs[0].Body, "KillMode=process") || !strings.Contains(fs[0].Body, "Environment=PATH=/a:/b") {
		t.Fatalf("service が違う:\n%s", fs[0].Body)
	}
	if !strings.Contains(fs[1].Body, "OnUnitInactiveSec=5min") {
		t.Fatalf("timer が違う:\n%s", fs[1].Body)
	}
	e.Exe = "/x/<t>&"
	if f := launchdFile("/L", e); !strings.Contains(f.Body, "<string>/x/&lt;t&gt;&amp;</string>") || !strings.HasSuffix(f.Path, ".plist") {
		t.Fatalf("plist のエスケープが違う:\n%s", f.Body)
	}
	if a := strings.Join(schtasksArgs(installEnv{Exe: `C:\t\tsumugi.exe`}), " "); !strings.Contains(a, `/TR "C:\t\tsumugi.exe" run`) {
		t.Fatalf("schtasks の引数が違う: %s", a)
	}
}

func TestListAndOnly(t *testing.T) {
	all := []agent{
		{Name: "a", ID: "old", StartedAt: 1},
		{Name: "a", ID: "new", StartedAt: 2, Pid: 9, Status: "idle"},
		{Name: "b", ID: "b1", StartedAt: 1},
	}
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{{Name: "a"}, {Name: "b"}}}}
	var buf bytes.Buffer
	printList(&buf, affs, all)
	if out := buf.String(); !strings.Contains(out, "new") || !strings.Contains(out, "止まっている") {
		t.Fatalf("list が違う:\n%s", out)
	}
	// 1 本だけ起こすときは、ほかのセッションを止めない
	st := map[string]affState{"x": {Sessions: map[string]sessionState{"b": {SessionID: "b1"}, "c": {SessionID: "c1"}}}}
	acts := plan([]affiliation{onlySession(affs[0], affs[0].Sessions[1])}, st, all)
	if len(acts) != 1 || acts[0].Name != "b" || acts[0].Verb != "起こし直す" {
		t.Fatalf("絞ったときのやることが違う: %+v", acts)
	}
}

func TestResolveName(t *testing.T) {
	affs := []affiliation{
		{ID: "a", Sessions: []sessionSpec{{Name: "x"}, {Name: "y"}}},
		{ID: "b", Sessions: []sessionSpec{{Name: "z"}}},
	}
	for arg, want := range map[string]string{"1": "x", "3": "z", "y": "y"} {
		if got, err := resolveName(affs, arg); err != nil || got != want {
			t.Fatalf("%s → %s %v（%s のはず）", arg, got, err, want)
		}
	}
	if _, err := resolveName(affs, "4"); err == nil {
		t.Fatal("無い番号を通した")
	}
}

func TestMachine(t *testing.T) {
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{
		{Name: "here", Prompt: "p"},
		{Name: "there", Machine: "other-pc", Prompt: "p"},
	}}}
	st := map[string]affState{"x": {Sessions: map[string]sessionState{"there": {SessionID: "s1"}}}}
	acts := planOn("my-pc", affs, st, nil)
	if len(acts) != 1 || acts[0].Name != "here" {
		t.Fatalf("よそのマシンのものは起こさず止めないはず: %+v", acts)
	}
	var buf bytes.Buffer
	printListOn(&buf, "my-pc", affs, nil)
	if out := buf.String(); !strings.Contains(out, "my-pc") || !strings.Contains(out, "（よそのマシン）") || !strings.Contains(out, "other-pc") {
		t.Fatalf("list にマシンが出ていない:\n%s", out)
	}
}

// 偽のデーモン: 長さの行＋本文を読み、決まった返りを返す。
func TestYuiAsk(t *testing.T) {
	c, d := net.Pipe()
	got := make(chan []string, 1)
	go func() {
		r := bufio.NewReader(d)
		var qs []string
		for i := 0; i < 3; i++ {
			var n int
			if _, err := fmt.Fscanf(r, "%d\n", &n); err != nil {
				break
			}
			b := make([]byte, n)
			io.ReadFull(r, b)
			qs = append(qs, string(b))
			end := `{"kind":"end","rc":0}`
			if i == 2 {
				end = `{"kind":"end","rc":0,"cut":true}` // rc=0 でも打ち切りは失敗に数える
			}
			fmt.Fprintf(d, "{\"kind\":\"node\",\"label\":\"x\"}\n%s\n", end)
		}
		got <- qs
		d.Close()
	}()
	var out bytes.Buffer
	ok, err := yuiAsk(c, &out, `a/b"c@m`, []string{"一覧 (決定:)", "壊れた"})
	if err != nil || ok {
		t.Fatalf("2 件目の打ち切り（cut）で ok=false のはず: %v %v", ok, err)
	}
	qs := <-got
	if len(qs) != 3 || qs[0] != `名乗る "a/b\"c@m"` || qs[1] != "一覧 (決定:)" {
		t.Fatalf("送ったものが違う: %q", qs)
	}
	if strings.Count(out.String(), "node") != 2 {
		t.Fatalf("問い合わせの返りだけ出すはず（名乗りの返りは出さない）:\n%s", out.String())
	}
}

func TestCron(t *testing.T) {
	c, err := parseCron("0 9,13,18 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	mon := time.Date(2026, 11, 16, 13, 0, 0, 0, time.Local) // 月曜
	if !c.match(mon) || c.match(mon.Add(time.Minute)) || c.match(mon.AddDate(0, 0, -1)) {
		t.Fatal("cron の当たりが違う")
	}
	for _, bad := range []string{"0 9 * *", "61 * * * *", "x * * * *", "*/0 * * * *"} {
		if _, err := parseCron(bad); err == nil {
			t.Fatalf("%q を通した", bad)
		}
	}
	if c, _ := parseCron("*/15 * * * 7"); !c.match(time.Date(2026, 11, 15, 10, 45, 0, 0, time.Local)) {
		t.Fatal("*/15 と日曜=7 が効いていない")
	}
}

func TestDueDeliveries(t *testing.T) {
	now := time.Date(2026, 11, 16, 13, 3, 0, 0, time.Local)
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{
		{Name: "a", Schedules: []scheduleSpec{
			{Cron: "0 13 * * *", Prompt: "昼"},
			{Cron: "0 9 * * *", Prompt: "朝"}, // 4 時間前＝猶予の外
			{Once: "2026-11-16 12:30", Prompt: "一度"},
		}},
		{Name: "never", Schedules: []scheduleSpec{{Cron: "0 13 * * *", Prompt: "p"}}},
	}}}
	st := map[string]affState{"x": {Sessions: map[string]sessionState{"a": {SessionID: "sa"}}}}
	ds := dueDeliveries("m", affs, st, []agent{{Name: "a", Pid: 1, ID: "ia", Status: "idle", StartedAt: 1}}, now)
	if len(ds) != 1 || ds[0].Prompt != "【定時】昼\n【定時】一度" || ds[0].Running == nil || len(ds[0].Keys) != 2 {
		t.Fatalf("届けるものが違う: %+v", ds)
	}
	// 届けたあとは、同じ発火を二度届けない
	ss := st["x"].Sessions["a"]
	ss.Delivered = ds[0].Keys
	st["x"].Sessions["a"] = ss
	if ds := dueDeliveries("m", affs, st, nil, now.Add(time.Minute)); len(ds) != 0 {
		t.Fatalf("二度届けている: %+v", ds)
	}
}

func TestThisMachineOrder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TSUMUGI_HOME", dir)
	t.Setenv("TSUMUGI_MACHINE", "")
	t.Setenv("WSL_DISTRO_NAME", "D")
	h, _ := os.Hostname()
	if got := thisMachine(); got != h+"/D" {
		t.Fatalf("環境から: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, globalFile), []byte(`{"マシン":"m/1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WSL_DISTRO_NAME", "")
	if got := thisMachine(); got != "m/1" {
		t.Fatalf("紡.json から: %q", got)
	}
	if machineUnpinnedOnWSL() {
		t.Fatal("決め打ちしてあるのに知らせる")
	}
	t.Setenv("TSUMUGI_MACHINE", "env/2")
	if got := thisMachine(); got != "env/2" {
		t.Fatalf("環境変数が先: %q", got)
	}
}
