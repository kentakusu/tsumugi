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
	return lockNamed(root, lockFile, wait)
}

// 更新（長いと何時間もかかる。winget など）は run とは別の錠で回す。
const updateLockFile = ".紡の更新の錠"

// 錠を持っている間は、1 分ごとに錠の時刻を新しくする（生きている印）。
// ★古さは「最後の印からの時間」で見る。印を打たずに作った時刻だけで見ていたので、
// 14 時間かかった更新の間、15 分ごとに錠が奪われて run が並走していた（2026-11-21 yui-windows の実測）。
const lockBeat = time.Minute

func lockNamed(root, name string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(root, name)
	end := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
			f.Close()
			stop := make(chan struct{})
			go func() {
				t := time.NewTicker(lockBeat)
				defer t.Stop()
				for {
					select {
					case <-stop:
						return
					case now := <-t.C:
						os.Chtimes(p, now, now)
					}
				}
			}()
			return func() { close(stop); os.Remove(p) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) > lockStale {
			os.Remove(p) // 古い錠を奪う
			continue
		}
		if b, err := os.ReadFile(p); err == nil && !lockHolderAlive(string(b)) {
			os.Remove(p) // 持ち主のプロセスがもう無い（WSL を起こし直したあとなど）。奪う
			continue
		}
		if time.Now().After(end) {
			return nil, fmt.Errorf("錠 %s が取れません（ほかの run・届けるが動いている）", p)
		}
		time.Sleep(time.Second)
	}
}
