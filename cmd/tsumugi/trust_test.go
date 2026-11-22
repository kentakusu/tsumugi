package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Claude Code の ~/.claude.json に似せたもの（大きな数・入れ子・ほかのフォルダの項・鍵の順）。
const sampleClaudeJSON = `{
  "numStartups": 103,
  "userID": "abc",
  "bigNumber": 12345678901234567890,
  "oauthAccount": {"emailAddress": "x@example.com", "note": "<a&b>"},
  "projects": {
    "/other": {
      "allowedTools": [],
      "hasTrustDialogAccepted": false,
      "lastCost": 1.5
    }
  },
  "zLast": [1, {"k": null}]
}
`

func readJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	o, err := parseOrdered(b)
	if err != nil {
		t.Fatalf("%s を読めない: %v", p, err)
	}
	return o.plain()
}

func backups(t *testing.T, root string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(root, "設定の控え", ".claude.json.bak-*"))
	return m
}

func trustSetup(t *testing.T, body string) (cfg, work, root string) {
	t.Helper()
	d := t.TempDir()
	cfg = filepath.Join(d, ".claude.json")
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	work = filepath.Join(d, "作業")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	work, _ = filepath.EvalSymlinks(work)
	root = filepath.Join(d, "手元")
	return
}

func TestTrustFolderWritesOnlyThatLeaf(t *testing.T) {
	cfg, work, root := trustSetup(t, sampleClaudeJSON)
	before := readJSON(t, cfg)
	wrote, err := trustFolder(cfg, work, root)
	if err != nil || !wrote {
		t.Fatalf("書くはず: wrote=%v err=%v", wrote, err)
	}
	after := readJSON(t, cfg)
	key := trustKey(work, "linux")
	if filepath.Separator == '\\' {
		key = trustKey(work, "windows")
	}
	p, _ := after["projects"].(map[string]any)[key].(map[string]any)
	if p[trustLeaf] != true || len(p) != 1 {
		t.Fatalf("その作業フォルダの印だけが true になるはず: %v", p)
	}
	// その 1 か所を除けば、書く前と同じ（ほかの鍵・ほかのフォルダの印は変わらない）
	delete(after["projects"].(map[string]any), key)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("ほかの鍵が変わった:\n前 %v\n後 %v", before, after)
	}
	b, _ := os.ReadFile(cfg)
	if !bytes.Contains(b, []byte("12345678901234567890")) {
		t.Fatal("大きな数が丸められた")
	}
	if i, j := bytes.Index(b, []byte(`"numStartups"`)), bytes.Index(b, []byte(`"zLast"`)); i < 0 || j < i {
		t.Fatal("鍵の順が変わった")
	}
	if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o644 && filepath.Separator == '/' {
		t.Fatalf("権限が変わった: %v", fi.Mode())
	}
	bs := backups(t, root)
	if len(bs) != 1 {
		t.Fatalf("控えが 1 つ残るはず: %v", bs)
	}
	if got, _ := os.ReadFile(bs[0]); string(got) != sampleClaudeJSON {
		t.Fatal("控えが書く前の中身と違う")
	}
	if fi, _ := os.Stat(bs[0]); fi.Mode().Perm() != 0o600 && filepath.Separator == '/' {
		t.Fatalf("控えは本人だけが読める権限のはず: %v", fi.Mode())
	}
	// 2 度目は書かない（控えも増えない）
	if wrote, err := trustFolder(cfg, work, root); err != nil || wrote {
		t.Fatalf("既に信頼済みなら書かないはず: wrote=%v err=%v", wrote, err)
	}
	if len(backups(t, root)) != 1 {
		t.Fatal("書いていないのに控えが増えた")
	}
	if m, _ := filepath.Glob(cfg + ".tsumugi-tmp.*"); len(m) != 0 {
		t.Fatalf("書きかけが残った: %v", m)
	}
}

func TestTrustFolderFlipsExistingEntry(t *testing.T) {
	d := t.TempDir()
	work, _ := filepath.EvalSymlinks(d)
	k, _ := json.Marshal(trustKey(work, "linux"))
	body := `{"projects":{` + string(k) + `:{"allowedTools":["Bash"],"hasTrustDialogAccepted":false,"z":1}}}`
	cfg := filepath.Join(t.TempDir(), ".claude.json")
	os.WriteFile(cfg, []byte(body), 0o600)
	if wrote, err := trustFolderOn("linux", cfg, work, t.TempDir()); err != nil || !wrote {
		t.Fatalf("wrote=%v err=%v", wrote, err)
	}
	p := readJSON(t, cfg)["projects"].(map[string]any)[trustKey(work, "linux")].(map[string]any)
	if p[trustLeaf] != true || p["z"] != float64(1) || len(p["allowedTools"].([]any)) != 1 {
		t.Fatalf("その項のほかの鍵は残るはず: %v", p)
	}
}

func TestTrustFolderStopsOnBrokenConfig(t *testing.T) {
	for name, body := range map[string]string{
		"壊れた JSON":      `{"projects": {`,
		"一番上が配列":        `[]`,
		"projects が文字列": `{"projects": "x"}`,
		"項がオブジェクトでない":   `{"projects": {"WORK": true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg, work, root := trustSetup(t, "")
			k, _ := json.Marshal(trustKey(work, "linux"))
			body = strings.Replace(body, `"WORK"`, string(k), 1)
			os.WriteFile(cfg, []byte(body), 0o644)
			if _, err := trustFolderOn("linux", cfg, work, root); err == nil {
				t.Fatal("止まるはず")
			}
			if b, _ := os.ReadFile(cfg); string(b) != body {
				t.Fatal("壊れたファイルを書き換えた")
			}
			if len(backups(t, root)) != 0 {
				t.Fatal("書いていないのに控えを残した")
			}
		})
	}
}

func TestTrustFolderNeedsConfigAndDir(t *testing.T) {
	d := t.TempDir()
	cfg := filepath.Join(d, ".claude.json")
	if _, err := trustFolderOn("linux", cfg, d, d); err == nil {
		t.Fatal("~/.claude.json が無いなら止まるはず")
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Fatal("~/.claude.json を作った")
	}
	os.WriteFile(cfg, []byte(`{}`), 0o600)
	if _, err := trustFolderOn("linux", cfg, filepath.Join(d, "無い"), d); err == nil {
		t.Fatal("在らない作業フォルダは信頼しないはず")
	}
	if b, _ := os.ReadFile(cfg); string(b) != `{}` {
		t.Fatal("書かないはずなのに書いた")
	}
}

func TestTrustKeyWindows(t *testing.T) {
	if got := trustKey(`D:\Users\kenta\x`, "windows"); got != "D:/Users/kenta/x" {
		t.Fatalf("Windows の鍵は / 区切りのはず: %s", got)
	}
	if got := trustKey("/home/a/x", "linux"); got != "/home/a/x" {
		t.Fatal(got)
	}
	o, _ := parseOrdered([]byte(`{"D:\\Users\\kenta\\x":{},"d:/users/kenta/x":{}}`))
	if got := matchKey(o, "D:/Users/kenta/x", "windows"); got != "d:/users/kenta/x" {
		t.Fatalf("大文字小文字だけ違う既存の鍵を使うはず（\\ の鍵は選ばない）: %s", got)
	}
	if got := matchKey(o, "D:/Users/kenta/x", "linux"); got != "D:/Users/kenta/x" {
		t.Fatalf("Windows でなければ大文字小文字を区別するはず: %s", got)
	}
}

// 「信頼する」: true のセッションだけが印を書く。false や書いていないものは書かない（対照）。
func TestTrustOnlyWhenAsked(t *testing.T) {
	root := t.TempDir()
	cfgDir := t.TempDir()
	t.Setenv("TSUMUGI_HOME", root)
	t.Setenv("TSUMUGI_MACHINE", "m")
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	cfg := filepath.Join(cfgDir, ".claude.json")
	os.WriteFile(cfg, []byte(`{"projects":{}}`), 0o600)
	dirs := map[string]string{}
	for _, n := range []string{"yes", "no", "none"} {
		d := filepath.Join(t.TempDir(), n)
		os.Mkdir(d, 0o700)
		dirs[n], _ = filepath.EvalSymlinks(d)
	}
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	writeAff(t, root, "x", `{"セッション":[
		{"名前":"yes","作業フォルダ":`+q(dirs["yes"])+`,"信頼する":true},
		{"名前":"no","作業フォルダ":`+q(dirs["no"])+`,"信頼する":false},
		{"名前":"none","作業フォルダ":`+q(dirs["none"])+`}]}`)
	affs, err := loadAffiliations(root)
	if err != nil {
		t.Fatal(err)
	}
	acts := planOn("m", affs, map[string]affState{"x": {Sessions: map[string]sessionState{}}}, nil)
	if len(acts) != 3 {
		t.Fatalf("3 つ起こすはず: %+v", acts)
	}
	for _, a := range acts {
		if a.Trust != (a.Name == "yes") {
			t.Fatalf("%s: Trust=%v", a.Name, a.Trust)
		}
		if !a.Trust {
			continue
		}
		// --dry では書かない
		if err := trustBeforeStart(io.Discard, a, true); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(cfg); string(b) != `{"projects":{}}` {
			t.Fatal("--dry なのに書いた")
		}
		if err := trustBeforeStart(io.Discard, a, false); err != nil {
			t.Fatal(err)
		}
	}
	ps := readJSON(t, cfg)["projects"].(map[string]any)
	if len(ps) != 1 {
		t.Fatalf("信頼する=true の作業フォルダだけが書かれるはず: %v", ps)
	}
	for n, d := range dirs {
		k := trustKey(d, "linux")
		if filepath.Separator == '\\' {
			k = trustKey(d, "windows")
		}
		_, ok := ps[k]
		if ok != (n == "yes") {
			t.Fatalf("%s: 書かれた=%v", n, ok)
		}
	}
	if len(backups(t, root)) != 1 {
		t.Fatal("控えが紡の置き場に残るはず")
	}
}

// 空の配列・空のオブジェクトは、そのまま書き戻す（null にしない）。
func TestOrderedKeepsEmpty(t *testing.T) {
	in := `{"a":[],"b":{},"c":[[]],"d":null}`
	o, err := parseOrdered([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(o)
	if string(b) != in {
		t.Fatalf("書き戻しが変わった: %s", b)
	}
}

// 実物の写しで確かめる（TSUMUGI_TRUST_REAL に写しのパスを渡したときだけ）。
func TestTrustRealCopy(t *testing.T) {
	p := os.Getenv("TSUMUGI_TRUST_REAL")
	if p == "" {
		t.Skip("TSUMUGI_TRUST_REAL が無い")
	}
	if wrote, err := trustFolder(p, t.TempDir(), t.TempDir()); err != nil || !wrote {
		t.Fatalf("wrote=%v err=%v", wrote, err)
	}
}
