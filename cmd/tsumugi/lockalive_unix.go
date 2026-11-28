//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// pid のプロセスが居るか・その名前（ps の comm）。分からなければ ok=false。テストで差し替える。
// Linux は /proc で見る（lockalive.go）ので、ここを使うのは macOS など。
var holderProcess = func(pid int) (exists bool, image string, ok bool) {
	if err := unix.Kill(pid, 0); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return false, "", true // その pid のプロセスは無い
		}
		if !errors.Is(err, unix.EPERM) {
			return false, "", false
		}
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return true, "", false
	}
	return true, strings.TrimSpace(string(out)), true
}
