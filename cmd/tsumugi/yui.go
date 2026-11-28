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
	"runtime"
	"sort"
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
	// 「個人」の層（使う人の決めたこと・好み・所属をまたぐ一般の決まり）。「結の層」の「個人」の別名。
	Yui yuiConf `json:"結"`
	// このマシンからつなげる結の層（名前 → つなぎ方）。例 {"個人": …7952, "useai": …7953}
	Layers map[string]yuiConf `json:"結の層,omitempty"`
}

// 所属.json の「結」。
type affYui struct {
	Default string   `json:"既定,omitempty"`  // 書くときの既定の層
	Allowed []string `json:"使える,omitempty"` // 使ってよい層（無ければ制限なし）
	// 旧い形（eefe9b3）: 所属だけの結のつなぎ方を直接書く。層の名前は所属の名前になる。
	yuiConf
}

// 結のデーモンへのつなぎ方。
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
			globalBroken(p, err)
			return g, fmt.Errorf("%s の書き方が違います: %w", p, err)
		}
		g = globalConfig{}
		if err := json.Unmarshal(b, &g); err != nil {
			globalBroken(p, err)
			return g, fmt.Errorf("%s の書き方が違います: %w", p, err)
		}
		warnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "tsumugi: %s に、この版の紡が知らない鍵があります（%v）。飛ばして読みます。紡が古いなら tsumugi 更新 紡\n", p, err)
		})
	}
	return g, nil
}

var warnOnce, brokenOnce sync.Once

// ★紡.json が壊れているのを、呼ぶ側が誤りを捨てても見えるように、標準エラーに 1 回出す
// （チャネル・更新・入れ直しは、読めなければ黙って既定に戻るため）。
func globalBroken(p string, err error) {
	brokenOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "tsumugi: ★%s が壊れていて読めません（%v）。マシン名・チャネル・更新・結の設定が既定に戻ります。直してください\n", p, err)
	})
}

// 結のデーモンのアドレス。環境変数 TSUMUGI_YUI が先。
const personalLayer = "個人"

// このマシンの結の層の一覧（紡.json の「結の層」と、「結」＝「個人」）。
func yuiLayers(g globalConfig) map[string]yuiConf {
	m := map[string]yuiConf{}
	for k, v := range g.Layers {
		m[k] = v
	}
	if _, ok := m[personalLayer]; !ok && g.Yui.Addr != "" {
		m[personalLayer] = g.Yui
	}
	if c, ok := m[personalLayer]; ok {
		if a := os.Getenv("TSUMUGI_YUI"); a != "" {
			c.Addr = a
			m[personalLayer] = c
		}
	}
	return m
}

// 所属の決まり: 既定の層と、使える層（nil なら制限なし）。
func affLayers(a *affiliation, layers map[string]yuiConf) (string, []string) {
	if a == nil || a.Yui == nil {
		return personalLayer, nil
	}
	y := a.Yui
	if y.Addr != "" { // 旧い形
		layers[a.ID] = y.yuiConf
		def := a.ID
		if y.Default != "" {
			def = y.Default
		}
		allowed := y.Allowed
		if allowed == nil {
			allowed = []string{personalLayer, a.ID}
		}
		return def, allowed
	}
	def := y.Default
	if def == "" {
		def = personalLayer
	}
	return def, y.Allowed
}

// どの層につなぐか。layer が空なら既定。戻り値: つなぎ方・層の名前・既定か。
func yuiTarget(layer string) (yuiConf, string, bool, error) {
	root, err := homeDir()
	if err != nil {
		return yuiConf{}, "", false, err
	}
	g, err := loadGlobal(root)
	if err != nil {
		return yuiConf{}, "", false, err
	}
	layers := yuiLayers(g)
	var af *affiliation
	if aff := os.Getenv("TSUMUGI_AFFILIATION"); aff != "" {
		if a, err := loadAffiliation(filepath.Join(root, aff, configFile)); err == nil {
			a.ID = aff
			af = &a
		}
	}
	def, allowed := affLayers(af, layers)
	if layer == "" {
		layer = def
	}
	if allowed != nil && !contains(allowed, layer) {
		return yuiConf{}, layer, false, fmt.Errorf("この所属（%s）は、結の「%s」の層を使えません。使える層: %s（所属.json の「結」「使える」で決める）",
			af.ID, layer, strings.Join(allowed, " / "))
	}
	c, ok := layers[layer]
	if !ok || c.Addr == "" {
		var names []string
		for k := range layers {
			names = append(names, k)
		}
		sort.Strings(names)
		return yuiConf{}, layer, false, fmt.Errorf("結の「%s」の層のつなぎ方がありません（%s の「結の層」）。このマシンの層: %s",
			layer, filepath.Join(root, globalFile), strings.Join(names, " / "))
	}
	return c, layer, layer == def, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
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

// 「個人」の層の合言葉。★紡.json が壊れていたら、それを返す（「合言葉が無い」と区別する。yui-windows の指摘）。
func yuiSecret() (string, error) {
	root, err := homeDir()
	if err != nil {
		return "", err
	}
	g, err := loadGlobal(root)
	if err != nil {
		return "", err
	}
	return yuiSecretOf(yuiLayers(g)[personalLayer])
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
	Text string `json:"text"`
}

// 1 本の接続で、名乗ってから問い合わせを順に投げる。返りの行はそのまま w へ。
// 戻り値は、どれか 1 つでも rc≠0 なら false。
func yuiAsk(conn io.ReadWriter, w io.Writer, secret, name string, queries []string) (bool, error) {
	r := bufio.NewReader(conn)
	ok := true
	lastErr := "" // 直近の error の行の文（画面に出さない問い合わせでも、失敗の説明に使う）
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
			if l.Kind == "error" {
				lastErr = l.Text
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
			// ★結の返した文を添える。「値が違う」と「この口は合言葉を見ない」（loopback の口に送った等）が、
			// 文を捨てると同じ「通りません」になり、正しい値を疑うことになる（2026-11-22 Reatex の層で実際に起きた）。
			// 紡は口の種類を判断しない。文に値が混ざっていても出さない。
			if lastErr != "" {
				return false, fmt.Errorf("合言葉が通りません（結の返り: %s）", strings.ReplaceAll(lastErr, secret, "＊＊＊"))
			}
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
	// 旗は --層 <名前>（--layer）と --共有（＝--層 個人）だけ。無ければ所属の既定の層。
	layer := ""
	switch {
	case len(args) > 0 && (args[0] == "--共有" || args[0] == "--shared"):
		layer, args = personalLayer, args[1:]
	case len(args) > 1 && (args[0] == "--層" || args[0] == "--layer"):
		layer, args = args[1], args[2:]
	}
	// ★結の言葉は「-」で始まらない。ほかの旗のようなものは、問い合わせとして結に送らずに断る
	// （yui コマンドの --read を付ける癖のあるセッションが、結の失敗として踏んでいた・Mac mini の報告）。
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("知らない旗です: %s（tsumugi yui の旗は --層 <名前> と --共有 だけです。--read なども要りません）", a)
		}
	}
	conf, layerName, isDefault, err := yuiTarget(layer)
	if err != nil {
		return err
	}
	where := "結の「" + layerName + "」の層"
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
			"★yui コマンドで .yui を直接開かないでください（デーモンが立てなくなります）。人に知らせて待ってください%s", where, addr, err, macLocalNetworkHint(runtime.GOOS, err))
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
	if !isDefault && writes(queries) {
		// ★既定でない層に書いたら、どこに書いたかを残す（止めずに、見えるように。yui-windows の提案）
		fmt.Fprintf(os.Stderr, "★%sに書きました（この所属の既定の層ではありません）\n", where)
	}
	if !ok {
		return fmt.Errorf("結が失敗を返しました（上の end の行）")
	}
	return nil
}

// 書き込む言葉を含むか（作る・結ぶ・消す・詰め直す）。
func writes(queries []string) bool {
	for _, q := range queries {
		for _, line := range strings.Split(q, "\n") {
			f := strings.Fields(line)
			if len(f) > 0 && (f[0] == "作る" || f[0] == "結ぶ" || f[0] == "消す" || f[0] == "詰め直す") {
				return true
			}
		}
	}
	return false
}
