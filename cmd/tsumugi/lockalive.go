package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// 錠の持ち主（錠のファイルの最初の語の pid）が、いまも紡として動いているか。
// ★WSL を起こし直すと、錠を持っていた run は消えるのに錠のファイルは残る。印の時刻だけで見ると、
// 15 分のあいだ run も届けるも動けない（2026-11-22 UseAI の再起動試験で実測: terminate の 4 秒前に取られた錠で、
// 19:24 と 19:32 の run が「錠が取れません」で止まり、useai-main が起こし直されなかった）。
// Linux では /proc/<pid>/cmdline に tsumugi があるかで見る（pid が別のプロセスに使い回されていても取り違えない）。
// 分からないとき（ほかの土台・読めない）は「動いている」とみなし、今までどおり印の時刻（lockStale）に任せる。
func lockHolderAlive(content string) bool {
	return lockHolderAliveOn(runtime.GOOS, content)
}

func lockHolderAliveOn(goos, content string) bool {
	if goos != "linux" {
		return true
	}
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return true
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return true
	}
	if pid == os.Getpid() {
		return true
	}
	b, err := os.ReadFile(procCmdline(pid))
	if err != nil {
		return !os.IsNotExist(err) // そのプロセスが無い → 落ちた
	}
	return strings.Contains(string(b), "tsumugi")
}

// テストで差し替える。
var procCmdline = func(pid int) string { return "/proc/" + strconv.Itoa(pid) + "/cmdline" }
