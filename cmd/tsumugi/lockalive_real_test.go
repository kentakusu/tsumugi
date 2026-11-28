package main

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// 本物のプロセスで: 終わったプロセスは「居ない」、動いている別のプロセスは名前が tsumugi でない。
func TestHolderProcessReal(t *testing.T) {
	var short, long *exec.Cmd
	if runtime.GOOS == "windows" {
		short, long = exec.Command("cmd.exe", "/c", "exit"), exec.Command("ping", "-n", "30", "127.0.0.1")
	} else {
		short, long = exec.Command("true"), exec.Command("sleep", "30")
	}
	if err := short.Run(); err != nil {
		t.Skip(err)
	}
	if exists, _, ok := holderProcess(short.Process.Pid); ok && exists {
		t.Fatalf("終わったプロセス %d を居るとした", short.Process.Pid)
	}
	if err := long.Start(); err != nil {
		t.Skip(err)
	}
	defer long.Process.Kill()
	exists, image, ok := holderProcess(long.Process.Pid)
	if !ok || !exists || strings.Contains(strings.ToLower(image), "tsumugi") {
		t.Fatalf("動いているプロセスの見え方が違う: %v %q %v", exists, image, ok)
	}
}
