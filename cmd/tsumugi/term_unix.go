//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Linux・macOS: pty（疑似端末）でプロセスを動かす。開き方だけ OS ごと（openPTY）。
type unixPTY struct {
	master *os.File
	cmd    *exec.Cmd
	done   chan struct{}
}

func startTerm(dir string, argv []string, env []string) (termProc, error) {
	master, slave, err := openPTY()
	if err != nil {
		return nil, err
	}
	unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 50, Col: 200})
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		return nil, err
	}
	slave.Close()
	p := &unixPTY{master: master, cmd: cmd, done: make(chan struct{})}
	go func() { cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *unixPTY) Read(b []byte) (int, error)  { return p.master.Read(b) }
func (p *unixPTY) Write(b []byte) (int, error) { return p.master.Write(b) }

func (p *unixPTY) Wait(d time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(d):
		return false
	}
}

func (p *unixPTY) Kill() {
	if !p.Wait(0) && p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
}

func (p *unixPTY) Close() { p.master.Close() }
