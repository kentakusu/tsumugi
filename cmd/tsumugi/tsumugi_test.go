package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		"a-run": {"s-run"}, "a-old": {"s-old"}, "a-gone": {"s-gone"},
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
	if a := strings.Join(got["a-new"].Args, " "); !strings.Contains(a, "-n a-new") || !strings.Contains(a, `"autoMemoryEnabled":false`) ||
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
