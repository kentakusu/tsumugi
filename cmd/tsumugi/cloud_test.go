package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubCloud(t *testing.T, create func(dir, prompt string) (string, error), send func(id, prompt string) error) {
	t.Helper()
	oc, os_ := cloudCreate, cloudSend
	t.Cleanup(func() { cloudCreate, cloudSend = oc, os_ })
	if create != nil {
		cloudCreate = create
	}
	if send != nil {
		cloudSend = send
	}
}

// マシン「クラウド」のセッション: 一度も起こしていない常駐のものだけ、クラウドで起こす。
// 控えの ID があれば、動いているかは見えないので何もしない（claude agents に出ないことを止まったと読まない）。
func TestPlanCloud(t *testing.T) {
	no := false
	affs := []affiliation{{ID: "k", Sessions: []sessionSpec{
		{Name: "new", Machine: "クラウド", Prompt: "p"},
		{Name: "known", Machine: "cloud", Prompt: "p"},
		{Name: "held", Machine: "クラウド", Prompt: "p"},
		{Name: "ondemand", Machine: "クラウド", Prompt: "p", Resident: &no},
		{Name: "local", Machine: "m", Prompt: "p"},
	}}}
	states := map[string]affState{"k": {Sessions: map[string]sessionState{
		"known": {SessionID: "session_known1234567"},
		"held":  {Held: true},
	}}}
	got := map[string]action{}
	for _, a := range planOn("よそ", affs, states, nil) {
		got[a.Name] = a
	}
	if a, ok := got["new"]; !ok || !a.Cloud || a.Verb != "クラウドで起こす" {
		t.Fatalf("一度も起こしていないクラウドのセッションを起こしていない: %+v", got)
	}
	for _, n := range []string{"known", "held", "ondemand", "local"} {
		if a, ok := got[n]; ok {
			t.Fatalf("%s を起こした: %+v", n, a)
		}
	}
	// 対照: 手元のセッションはこの機（m）なら起こす
	if acts := planOn("m", affs, states, nil); len(acts) < 2 {
		t.Fatalf("手元のセッションを起こしていない（対照）: %+v", acts)
	}
}

// 定時・届けは claude -p --cloud で積む。積めなければ「渡せなかった」として残す。
func TestDeliverCloud(t *testing.T) {
	var gotID, gotPrompt string
	fail := false
	stubCloud(t, nil, func(id, prompt string) error {
		gotID, gotPrompt = id, prompt
		if fail {
			return errors.New("archived")
		}
		return nil
	})
	oldRun := runClaudeIn
	t.Cleanup(func() { runClaudeIn = oldRun })
	runClaudeIn = func(string, ...string) ([]byte, error) { t.Fatal("手元の claude を呼んだ"); return nil, nil }
	d := delivery{Affiliation: "k", Name: "c", SessionID: "session_abc1234567", Prompt: "【定時】朝", spec: sessionSpec{Name: "c", Machine: "クラウド"}}
	var out bytes.Buffer
	ok, newID := deliverPrompt(&out, d)
	if !ok || newID != "" || gotID != "session_abc1234567" || gotPrompt != "【定時】朝" {
		t.Fatalf("クラウドへ積んでいない: %v %q %q %q %s", ok, newID, gotID, gotPrompt, out.String())
	}
	if !strings.Contains(out.String(), "確かめられない") {
		t.Fatalf("確かめていないことを出していない: %s", out.String())
	}
	fail = true
	out.Reset()
	if ok, _ := deliverPrompt(&out, d); ok {
		t.Fatalf("積めなかったのに渡せたとした: %s", out.String())
	}
}

// 常駐しないクラウドのセッションは、定時・届けのときに、最初の指示と渡す指示を一緒にしてクラウドで起こす。
func TestStartFreshCloud(t *testing.T) {
	var got string
	stubCloud(t, func(dir, prompt string) (string, error) { got = prompt; return "session_new1234567", nil }, nil)
	var out bytes.Buffer
	id, ok := startFresh(&out, affiliation{ID: "k"}, sessionSpec{Name: "c", Machine: "クラウド", Prompt: "最初の指示"}, "【届け】これ")
	if !ok || id != "session_new1234567" {
		t.Fatalf("%v %q %s", ok, id, out.String())
	}
	for _, want := range []string{"所属 k のセッション「c」", "クラウド", "最初の指示", "【届け】これ"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q が指示に無い: %s", want, got)
		}
	}
}

// 作業フォルダが git のリポジトリでなければ、クラウドに立てない（立てる前に断る）。
func TestCloudCreateNeedsRepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := cloudCreate(dir, "x"); err == nil || !strings.Contains(err.Error(), "git のリポジトリではありません") {
		t.Fatalf("git でないフォルダを断っていない: %v", err)
	}
	os.MkdirAll(filepath.Join(dir, ".git"), 0o700) // 対照の形だけ（実際には立てない）
}

// 一覧: クラウドのセッションはマシン「クラウド」で、ID を全部出し、動いているかは見えないと出す。
func TestListCloud(t *testing.T) {
	affs := []affiliation{{ID: "k", Sessions: []sessionSpec{
		{Name: "c1", Machine: "クラウド"},
		{Name: "c2", Machine: "クラウド"},
	}}}
	states := map[string]affState{"k": {Sessions: map[string]sessionState{"c1": {SessionID: "session_abc1234567"}}}}
	var b bytes.Buffer
	printListFull(&b, "m", affs, states, nil)
	out := b.String()
	for _, want := range []string{"c1", "クラウド（動いているかは見えない）", "session_abc1234567", "c2", "まだ起こしていない（次の run がクラウドで起こす）"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q が出ていない:\n%s", want, out)
		}
	}
	if strings.Contains(out, "よそのマシン") {
		t.Fatalf("クラウドをよそのマシンと出した:\n%s", out)
	}
}
