package main

// 疑似端末（Windows は ConPTY・Linux と macOS は pty）の中で 1 つのコマンドを動かし、画面に出たものを集める。
//
// ★claude --cloud "<指示>" は、新しいクラウドのセッションを対話の画面でしか立てない（-p と組めない・2.1.331）。
// 紡は人の画面を使わず、自分で疑似端末を持って動かす（tmux などには頼らない）。

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"sync"
	"time"
)

// 疑似端末の中で動いている 1 つのプロセス。
type termProc interface {
	io.Reader                  // 画面に出たもの
	io.Writer                  // キーボードから入れたもの
	Wait(d time.Duration) bool // 終わるまで待つ（d 以内に終われば true）
	Kill()
	Close() // 疑似端末を閉じる（読み終わったあと）
}

// 画面の問い合わせ（いまのカーソルはどこか）。答えないと待ち続ける画面がある。
var cursorQuery = []byte("\x1b[6n")

// argv を疑似端末で動かし、done が真を返すか、終わるか、timeout まで画面を集める。集めた画面（制御文字を除いたもの）を返す。
func runInTerminal(dir string, argv []string, env []string, timeout time.Duration, done func(screen string) bool) (string, error) {
	p, err := startTerm(dir, argv, env)
	if err != nil {
		return "", err
	}
	var mu sync.Mutex
	var raw bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			n, err := p.Read(buf)
			if n > 0 {
				mu.Lock()
				raw.Write(buf[:n])
				mu.Unlock()
				if bytes.Contains(buf[:n], cursorQuery) {
					p.Write([]byte("\x1b[1;1R"))
				}
			}
			if err != nil {
				return
			}
		}
	}()
	screen := func() string {
		mu.Lock()
		defer mu.Unlock()
		return stripTerm(raw.String())
	}
	var result error
	end := time.Now().Add(timeout)
	for {
		if p.Wait(200 * time.Millisecond) {
			break
		}
		if done != nil && done(screen()) {
			break
		}
		if time.Now().After(end) {
			result = errors.New("時間内に終わりませんでした")
			break
		}
	}
	p.Kill()
	p.Close()
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
	}
	return screen(), result
}

var termEscape = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78DEHMNOc]`)

// 画面の制御文字を除く（行の区切りは残す）。
func stripTerm(s string) string {
	s = termEscape.ReplaceAllString(s, "")
	return regexp.MustCompile(`\r\n?`).ReplaceAllString(s, "\n")
}
