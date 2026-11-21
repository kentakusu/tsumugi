package main

// 結（yui）のデーモンに問い合わせる口。紡のセッションは結をこれで読み書きする。
//
// ★必ずデーモンを通す。デーモンにつながらないとき、yui コマンドでファイルを直接開きに
// 行かない（デーモンの錠が無い隙に開くと、次にデーモンが立てなくなる）。止めて知らせる。
//
// やりとり（結の README「デーモン」）: 送る「<バイト数>\n<本文>」、返る JSON の行。
// 締めの行 {"kind":"end","rc":N} が来るまで読む。

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 手元の全体設定（<手元>/紡.json）。所属をまたぐもの。
type globalConfig struct {
	// このマシンの名前（無ければホスト名から作る。thisMachine を見る）
	Machine string `json:"マシン,omitempty"`
	// チャネル（動いているセッションへ止めずに渡す）を使うか。使うなら、紡が起こすセッションに --channels を付ける。
	// マシンの管理設定に allowedChannelPlugins が要る（channel.go）。
	Channel bool `json:"チャネル,omitempty"`
	// Claude Code のユーザー設定のうち、基本の値（settings/claude-base.json）に合わせてよい鍵。"*" で全部。
	// 無ければ、ずれを出すだけ（settingsalign.go）。
	AlignSettings []string `json:"設定を合わせる,omitempty"`
	// 更新（Claude Code と環境を新しくする）。update.go
	Update *updateSpec `json:"更新,omitempty"`
	Yui    struct {
		Addr string `json:"アドレス"` // 例 127.0.0.1:7952
	} `json:"結"`
}

const globalFile = "紡.json"

func loadGlobal(root string) (globalConfig, error) {
	var g globalConfig
	p := filepath.Join(root, globalFile)
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return g, nil
	}
	if err != nil {
		return g, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&g); err != nil {
		return g, fmt.Errorf("%s の書き方が違います: %w", p, err)
	}
	return g, nil
}

// 結のデーモンのアドレス。環境変数 TSUMUGI_YUI が先。
func yuiAddr() (string, error) {
	if a := os.Getenv("TSUMUGI_YUI"); a != "" {
		return a, nil
	}
	root, err := homeDir()
	if err != nil {
		return "", err
	}
	g, err := loadGlobal(root)
	if err != nil {
		return "", err
	}
	if g.Yui.Addr == "" {
		return "", fmt.Errorf("結のデーモンのアドレスが決まっていません（%s の「結」「アドレス」か、環境変数 TSUMUGI_YUI）", filepath.Join(root, globalFile))
	}
	return g.Yui.Addr, nil
}

// 名乗り。紡が起こしたセッションは「<所属>/<名前>@<マシン>」、それ以外は「人:<ユーザー>@<マシン>」。
func yuiName() string {
	if n := os.Getenv("TSUMUGI_SESSION"); n != "" {
		if a := os.Getenv("TSUMUGI_AFFILIATION"); a != "" {
			n = a + "/" + n
		}
		return n + "@" + thisMachine()
	}
	u := os.Getenv("USER")
	if u == "" {
		u = os.Getenv("USERNAME")
	}
	return "人:" + u + "@" + thisMachine()
}

// 結の言葉の文字列に入れる（" と \ を逃がす）。
func yuiQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

type yuiLine struct {
	Kind string `json:"kind"`
	RC   *int   `json:"rc"`
	Cut  bool   `json:"cut"` // 返りが上限で打ち切られた（これで全部ではない）
}

// 1 本の接続で、名乗ってから問い合わせを順に投げる。返りの行はそのまま w へ。
// 戻り値は、どれか 1 つでも rc≠0 なら false。
func yuiAsk(conn io.ReadWriter, w io.Writer, name string, queries []string) (bool, error) {
	r := bufio.NewReader(conn)
	ok := true
	send := func(q string, echo bool) error {
		if _, err := fmt.Fprintf(conn, "%d\n%s", len(q), q); err != nil {
			return err
		}
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return fmt.Errorf("締めの行（end）の前に切れました: %w", err)
			}
			var l yuiLine
			if json.Unmarshal([]byte(line), &l) != nil {
				return fmt.Errorf("読めない返りです: %q", line)
			}
			if echo || (l.Kind == "end" && l.RC != nil && *l.RC != 0) {
				io.WriteString(w, line)
			}
			if l.Kind == "end" {
				if l.RC == nil || *l.RC != 0 {
					ok = false
				}
				if l.Cut {
					// ★打ち切りを「これで全部」と読ませない
					fmt.Fprintf(os.Stderr, "tsumugi: 返りが上限で打ち切られました（これで全部ではありません）: %s\n", q)
					ok = false
				}
				return nil
			}
		}
	}
	if err := send("名乗る "+yuiQuote(name), false); err != nil {
		return false, err
	}
	for _, q := range queries {
		if err := send(q, true); err != nil {
			return false, err
		}
	}
	return ok, nil
}

// tsumugi yui <問い合わせ>…（無ければ標準入力の全体を 1 件として）
func cmdYui(args []string) error {
	addr, err := yuiAddr()
	if err != nil {
		return err
	}
	queries := args
	if len(queries) == 0 {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		if q := strings.TrimRight(string(b), "\r\n"); q != "" {
			queries = []string{q}
		}
	}
	if len(queries) == 0 {
		return fmt.Errorf("問い合わせがありません")
	}
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("結のデーモン（%s）につながりません: %w\n"+
			"★yui コマンドで .yui を直接開かないでください（デーモンが立てなくなります）。人に知らせて待ってください", addr, err)
	}
	defer conn.Close()
	ok, err := yuiAsk(conn, os.Stdout, yuiName(), queries)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("結が失敗を返しました（上の end の行）")
	}
	return nil
}
