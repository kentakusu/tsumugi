package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 疑似端末で動かしたプロセスの画面が読めること・画面の問い合わせに答えること。
// 本物の端末を作るので、土台の道具（Windows は cmd、ほかは sh）で通す。
func TestRunInTerminal(t *testing.T) {
	var argv []string
	if runtime.GOOS == "windows" {
		argv = []string{"cmd.exe", "/c", "echo 紡の試し"}
	} else {
		if _, err := exec.LookPath("bash"); err != nil {
			t.Skip("bash が無い")
		}
		// 画面の問い合わせ（ESC[6n）を出し、答え（ESC[1;1R の 6 文字）を改行を待たずに読む。答えないと止まる
		argv = []string{"bash", "-c", "printf '\\033[6n'; read -r -s -n 6 x; echo 紡の試し"}
	}
	out, err := runInTerminal("", argv, os.Environ(), 20*time.Second, nil)
	if err != nil {
		t.Fatalf("%v（画面: %q）", err, out)
	}
	if !strings.Contains(out, "紡の試し") {
		t.Fatalf("画面に出たものが読めていない: %q", out)
	}
}

// 画面の制御文字を除く。
func TestStripTerm(t *testing.T) {
	in := "\x1b[?25l\x1b[2J\x1b[HCreated cloud session: x\r\n\x1b]0;title\x07View: https://claude.ai/code/session_ABC123\x1b[0m\r\n"
	got := stripTerm(in)
	if !strings.Contains(got, "Created cloud session: x\n") || !strings.Contains(got, "View: https://claude.ai/code/session_ABC123\n") || strings.Contains(got, "\x1b") {
		t.Fatalf("%q", got)
	}
}
