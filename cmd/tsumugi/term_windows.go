//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows: ConPTY（疑似コンソール）でプロセスを動かす。
type conpty struct {
	hpc     windows.Handle
	in, out *os.File
	proc    windows.Handle
	closed  bool
}

func startTerm(dir string, argv []string, env []string) (termProc, error) {
	exe, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, err
	}
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, err
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, err
	}
	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: 200, Y: 50}, inR, outW, 0, &hpc); err != nil {
		for _, h := range []windows.Handle{inR, inW, outR, outW} {
			windows.CloseHandle(h)
		}
		return nil, fmt.Errorf("疑似コンソールを作れません: %w", err)
	}
	// 疑似コンソールが持ったので、こちら側の端は閉じる
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)
	c := &conpty{hpc: hpc, in: os.NewFile(uintptr(inW), "conpty-in"), out: os.NewFile(uintptr(outR), "conpty-out")}

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		c.Close()
		return nil, err
	}
	defer attrs.Delete()
	// 値は HPCON そのもの（指す先ではない）
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&hpc)), unsafe.Sizeof(hpc)); err != nil {
		c.Close()
		return nil, err
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	// ★呼んだ側の標準入出力（パイプ・ファイル）を子に渡さない。渡すと子の画面が疑似コンソールに出ない
	si.Flags = windows.STARTF_USESTDHANDLES

	args := append([]string{exe}, argv[1:]...)
	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		c.Close()
		return nil, err
	}
	var dirp *uint16
	if dir != "" {
		if dirp, err = windows.UTF16PtrFromString(dir); err != nil {
			c.Close()
			return nil, err
		}
	}
	var envp *uint16
	if env != nil {
		block := envBlock(env)
		envp = &block[0]
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcess(nil, cmdline, nil, nil, false, flags, envp, dirp, &si.StartupInfo, &pi); err != nil {
		c.Close()
		return nil, fmt.Errorf("%s を起こせません: %w", exe, err)
	}
	windows.CloseHandle(pi.Thread)
	c.proc = pi.Process
	return c, nil
}

// 環境変数の並び（"K=V\0...\0\0"）。
func envBlock(env []string) []uint16 {
	var b []uint16
	for _, e := range env {
		if strings.ContainsRune(e, 0) {
			continue
		}
		b = append(b, utf16.Encode([]rune(e))...)
		b = append(b, 0)
	}
	if len(b) == 0 {
		b = append(b, 0)
	}
	return append(b, 0)
}

func (c *conpty) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *conpty) Write(p []byte) (int, error) { return c.in.Write(p) }

func (c *conpty) Wait(d time.Duration) bool {
	if c.proc == 0 {
		return true
	}
	ev, _ := windows.WaitForSingleObject(c.proc, uint32(d/time.Millisecond))
	return ev == windows.WAIT_OBJECT_0
}

func (c *conpty) Kill() {
	if c.proc != 0 && !c.Wait(0) {
		windows.TerminateProcess(c.proc, 1)
	}
}

func (c *conpty) Close() {
	if c.closed {
		return
	}
	c.closed = true
	// ★ClosePseudoConsole は出力を吐き切るまで待つので、読む側（out）は閉じる前まで読み続けている必要がある
	if c.hpc != 0 {
		windows.ClosePseudoConsole(c.hpc)
	}
	if c.in != nil {
		c.in.Close()
	}
	if c.out != nil {
		c.out.Close()
	}
	if c.proc != 0 {
		windows.CloseHandle(c.proc)
	}
}
