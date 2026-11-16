package main

// 紡の錠: run と 届ける が同じセッションを同時に止めたり起こしたりしないように、
// 手元（~/.tsumugi）に 1 つだけ置く。作れたら持ち主。古い錠（持ち主が落ちた）は奪う。

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const lockFile = ".紡の錠" // 結の .錠 と取り違えないよう名前を変える

// これより古い錠は、持ち主が落ちたものとみなす（run 1 回は長くても数分）。
const lockStale = 15 * time.Minute

func lockHome(root string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(root, lockFile)
	end := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) > lockStale {
			os.Remove(p) // 古い錠を奪う
			continue
		}
		if time.Now().After(end) {
			return nil, fmt.Errorf("錠 %s が取れません（ほかの run・届けるが動いている）", p)
		}
		time.Sleep(time.Second)
	}
}
