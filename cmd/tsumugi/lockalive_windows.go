//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

// pid のプロセスが居るか・その実行ファイルの道。分からなければ ok=false。テストで差し替える。
var holderProcess = func(pid int) (exists bool, image string, ok bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, "", true // その pid のプロセスは無い
		}
		return false, "", false // 権限などで見えない
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err == nil && code != 259 { // 259 = STILL_ACTIVE
		return false, "", true
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return true, "", false
	}
	return true, windows.UTF16ToString(buf[:n]), true
}
