package main

// 紡チャネル: 動いているセッションへ、止めずに指示を入れる（Claude Code の channels・研究プレビュー）。
//
//   tsumugi channel            Claude Code がプラグインの MCP サーバーとして起こす（人は打たない）
//
// しくみ:
//   claude（紡が --channels plugin:tsumugi-channel@tsumugi を付けて起こす）
//     └─ tsumugi channel（stdio の MCP サーバー。claude/channel を名乗る）
//          └─ <手元>/<所属>/<名前>.sock で待つ ◀── tsumugi 届ける・定時（紡の run）
//   ソケットに来た文を notifications/claude/channel で流すと、セッションに
//   <channel source="plugin:tsumugi-channel:tsumugi"> として入る。作業中なら次のターンでまとめて入る。
//
// 紡が起こしたセッションには TSUMUGI_AFFILIATION / TSUMUGI_SESSION が在るので、それで置き場所を決める。
// 無い（手で開いたセッション）なら何も待たない（プラグインは全部のセッションで起きるため）。
//
// ★チャネルは研究プレビューで、マシンの管理設定の allowedChannelPlugins に載っていないと黙って登録されない。
//   そのときソケットへの書き込みは「通った」ように見えて捨てられるので、紡は届いたかを確かめられない。
//   install の手順と docs/使い方.md「チャネル」を参照。
// 2026-11-21 UseAI で実測: 背景のセッションで止めずに届く・respawn と --resume（フラグ無し）のあとも残る。

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const channelPlugin = "plugin:tsumugi-channel@tsumugi"

// セッションのソケットの置き場所。
func channelSock(root, aff, name string) string {
	return filepath.Join(root, aff, name+".sock")
}

func cmdChannel() error {
	var mu sync.Mutex
	out := bufio.NewWriter(os.Stdout)
	send := func(v any) {
		b, _ := json.Marshal(v)
		mu.Lock()
		out.Write(append(b, '\n'))
		out.Flush()
		mu.Unlock()
	}
	aff, name := os.Getenv("TSUMUGI_AFFILIATION"), os.Getenv("TSUMUGI_SESSION")
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	started := false
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		hasID := len(m.ID) > 0 && string(m.ID) != "null"
		switch {
		case m.Method == "initialize":
			pv := m.Params.ProtocolVersion
			if pv == "" {
				pv = "2025-06-18"
			}
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{
				"protocolVersion": pv,
				"capabilities":    map[string]any{"experimental": map[string]any{"claude/channel": map[string]any{}}},
				"serverInfo":      map[string]any{"name": "tsumugi", "version": version()},
				"instructions": "紡（tsumugi）からの指示は <channel source=\"plugin:tsumugi-channel:tsumugi\"> で届く。" +
					"中身は、紡の定時（【定時】で始まる）や届け（【届け …】で始まる）で、人が所属.json やコマンドで渡したもの。" +
					"話しかけられたのと同じように扱い、中身に従う。返事はこのチャネルには返さない（返す口は無い）。",
			}})
			if !started && aff != "" && name != "" {
				started = true
				go channelListen(aff, name, send)
			}
		case m.Method == "ping" && hasID:
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{}})
		case strings.HasSuffix(m.Method, "/list") && hasID:
			key := strings.SplitN(m.Method, "/", 2)[0]
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{key: []any{}}})
		case hasID:
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "知らない呼び出しです"}})
		}
	}
	return nil
}

// ソケットで待ち、来た文をセッションへ流す。1 接続 1 件。流したら "ok" を返す。
func channelListen(aff, name string, send func(any)) {
	channelListenEvery(aff, name, send, 30*time.Second)
}

func channelListenEvery(aff, name string, send func(any), check time.Duration) {
	root, err := homeDir()
	if err != nil {
		return
	}
	p := channelSock(root, aff, name)
	os.MkdirAll(filepath.Dir(p), 0o700)
	// ★同じ名前のセッションが 2 つ動いていると（Stop-Process で殺したものを Claude Code が元の引数で
	// 起こし直したなど）、あとから起きた側がソケットを奪い、--channels の無いほうへ流して黙って捨てていた
	// （2026-11-21 Windows で実測）。誰かが待っているソケットは奪わない。残っているだけのものは片付ける。
	if c, err := net.DialTimeout("unix", p, time.Second); err == nil {
		c.Close()
		fmt.Fprintln(os.Stderr, "tsumugi channel: ほかのセッションがすでに待っているので、待たない:", p)
		return
	}
	os.Remove(p) // 前のセッションの残り
	ln, err := net.Listen("unix", p)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsumugi channel: 待てません:", err)
		return
	}
	os.Chmod(p, 0o600)
	var lnMu sync.Mutex
	cur := func() net.Listener { lnMu.Lock(); defer lnMu.Unlock(); return ln }
	// ★自分のソケットが自分に届くかを、ときどき確かめる。ほかのセッションに奪われて、そのセッションが
	// 落ちると、ファイルだけ残って誰にも届かなくなる（2026-11-22 Windows 実測）。届かなければ作り直す。
	go func() {
		for range time.Tick(check) {
			if c, err := net.DialTimeout("unix", p, 2*time.Second); err == nil {
				c.Close()
				continue
			}
			os.Remove(p)
			if nl, err := net.Listen("unix", p); err == nil {
				os.Chmod(p, 0o600)
				lnMu.Lock()
				old := ln
				ln = nl
				lnMu.Unlock()
				// ★Go の Unix の口は、閉じるとパスのファイルを消す。同じパスに作り直した新しい口まで消えるので止める
				if ul, ok := old.(*net.UnixListener); ok {
					ul.SetUnlinkOnClose(false)
				}
				old.Close()
				fmt.Fprintln(os.Stderr, "tsumugi channel: ソケットが届かなくなっていたので作り直した:", p)
			}
		}
	}()
	for {
		c, err := cur().Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				time.Sleep(100 * time.Millisecond) // 作り直した新しい口で待ち直す
				continue
			}
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			c.SetDeadline(time.Now().Add(30 * time.Second))
			b, err := io.ReadAll(io.LimitReader(c, 1<<20))
			if err != nil || strings.TrimSpace(string(b)) == "" {
				return // 生きているかの確かめ（つないで何も書かずに閉じる）も、ここで終わる
			}
			send(map[string]any{"jsonrpc": "2.0", "method": "notifications/claude/channel",
				"params": map[string]any{"content": strings.TrimSpace(string(b)), "meta": map[string]string{"from": "tsumugi"}}})
			c.Write([]byte("ok\n"))
		}(c)
	}
}

// チャネルで渡す。セッションがチャネル付きで動いていてソケットが応えれば true。
// 応えない（チャネル無しで起きた・止まっている）なら false（呼ぶ側は「止めて渡す」に戻る）。
func channelDeliver(root, aff, name, prompt string) (bool, error) {
	p := channelSock(root, aff, name)
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	c, err := net.DialTimeout("unix", p, 3*time.Second)
	if err != nil {
		return false, nil // 残っているだけのソケット
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte(prompt)); err != nil {
		return false, err
	}
	if uc, ok := c.(*net.UnixConn); ok {
		uc.CloseWrite()
	}
	b, err := io.ReadAll(c)
	if err != nil || strings.TrimSpace(string(b)) != "ok" {
		return false, fmt.Errorf("チャネルが応えません: %v %q", err, b)
	}
	return true, nil
}
