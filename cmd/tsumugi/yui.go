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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	// 共有の結（全所属から読める。使う人の決めたこと・好み・一般の決まり）。
	Yui yuiConf `json:"結"`
}

// 結のデーモンへのつなぎ方。紡.json の「結」は共有の結、所属.json の「結」はその所属だけの結。
type yuiConf struct {
	Addr string `json:"アドレス"` // 例 127.0.0.1:7952
	// 別の筐体から LAN の口（--lan）につなぐときの合言葉の出どころ。★値そのものは書かない。
	// どちらか片方: ファイル（本人だけが読める権限で置く）か、値を標準出力に出すコマンド
	// （macOS の Keychain なら ["security","find-generic-password","-s","tsumugi-yui","-w"]）。
	SecretFile string   `json:"合言葉ファイル,omitempty"`
	SecretCmd  []string `json:"合言葉コマンド,omitempty"`
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
		// ★知らない鍵は、新しい版の紡が足したものかもしれない。厳密に断ると、古い版の紡では
		// マシンの名前・チャネル・更新・結のアドレスが全部読めなくなり、黙って振る舞いが変わる
		// （2026-11-22 Windows で、結への道が 6 時間 45 分 閉じた）。知らない鍵は飛ばして読み、知らせる。
		if !strings.Contains(err.Error(), "unknown field") {
			return g, fmt.Errorf("%s の書き方が違います: %w", p, err)
		}
		g = globalConfig{}
		if err := json.Unmarshal(b, &g); err != nil {
			return g, fmt.Errorf("%s の書き方が違います: %w", p, err)
		}
		warnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "tsumugi: %s に、この版の紡が知らない鍵があります（%v）。飛ばして読みます。紡が古いなら tsumugi 更新 紡\n", p, err)
		})
	}
	return g, nil
}

var warnOnce sync.Once

// 結のデーモンのアドレス。環境変数 TSUMUGI_YUI が先。
// どの結につなぐか。shared なら共有の結（紡.json）。そうでなければ、このセッションの所属に
// 「結」があればそれ、無ければ共有の結。戻り値の where は人に見せる名前。
func yuiTarget(shared bool) (yuiConf, string, error) {
	root, err := homeDir()
	if err != nil {
		return yuiConf{}, "", err
	}
	if !shared {
		if aff := os.Getenv("TSUMUGI_AFFILIATION"); aff != "" {
			if a, err := loadAffiliation(filepath.Join(root, aff, configFile)); err == nil && a.Yui != nil && a.Yui.Addr != "" {
				return *a.Yui, "所属「" + aff + "」の結", nil
			}
		}
	}
	g, err := loadGlobal(root)
	if err != nil {
		return yuiConf{}, "", err
	}
	c := g.Yui
	if a := os.Getenv("TSUMUGI_YUI"); a != "" {
		c.Addr = a
	}
	if c.Addr == "" {
		return c, "", fmt.Errorf("結のデーモンのアドレスが決まっていません（%s の「結」「アドレス」か、環境変数 TSUMUGI_YUI）", filepath.Join(root, globalFile))
	}
	return c, "共有の結", nil
}

// 合言葉（LAN の口のとき）。127.0.0.1 の口なら要らない（空を返す）。
// 環境変数 TSUMUGI_YUI_SECRET_FILE があれば、それが先。
func yuiSecretOf(c yuiConf) (string, error) {
	file := c.SecretFile
	if f := os.Getenv("TSUMUGI_YUI_SECRET_FILE"); f != "" {
		file = f
	}
	switch {
	case file != "":
		b, err := os.ReadFile(expandHome(file))
		if err != nil {
			return "", fmt.Errorf("合言葉のファイルを読めません: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	case len(c.SecretCmd) > 0:
		out, err := exec.Command(expandHome(c.SecretCmd[0]), c.SecretCmd[1:]...).Output()
		if err != nil {
			return "", fmt.Errorf("合言葉のコマンドが失敗しました: %w", err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	return "", nil
}

// 共有の結の合言葉（テストと互換のため）。
func yuiSecret() (string, error) {
	c, _, err := yuiTarget(true)
	if err != nil {
		root, _ := homeDir()
		g, _ := loadGlobal(root)
		c = g.Yui
	}
	return yuiSecretOf(c)
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
func yuiAsk(conn io.ReadWriter, w io.Writer, secret, name string, queries []string) (bool, error) {
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
	if secret != "" {
		// LAN の口は、最初の 1 行で合言葉を送る（合うまで読むことも通らない）。★値は画面にも記録にも出さない
		if err := send("合言葉 "+secret, false); err != nil {
			return false, fmt.Errorf("合言葉の返りがありません（違うと、その場で切られます）: %w", err)
		}
		if !ok {
			return false, fmt.Errorf("合言葉が通りません")
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
	// 旗は --共有（--shared）だけ。共有の結（全所属から読める）につなぐ。無ければ所属の結（無ければ共有）。
	shared := false
	if len(args) > 0 && (args[0] == "--共有" || args[0] == "--shared") {
		shared, args = true, args[1:]
	}
	// ★結の言葉は「-」で始まらない。ほかの旗のようなものは、問い合わせとして結に送らずに断る
	// （yui コマンドの --read を付ける癖のあるセッションが、結の失敗として踏んでいた・Mac mini の報告）。
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("知らない旗です: %s（tsumugi yui の旗は --共有 だけです。--read なども要りません）", a)
		}
	}
	conf, where, err := yuiTarget(shared)
	if err != nil {
		return err
	}
	addr := conf.Addr
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
		return fmt.Errorf("%s（%s）につながりません: %w\n"+
			"★yui コマンドで .yui を直接開かないでください（デーモンが立てなくなります）。人に知らせて待ってください", where, addr, err)
	}
	defer conn.Close()
	secret, err := yuiSecretOf(conf)
	if err != nil {
		return err
	}
	ok, err := yuiAsk(conn, os.Stdout, secret, yuiName(), queries)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("結が失敗を返しました（上の end の行）")
	}
	return nil
}
