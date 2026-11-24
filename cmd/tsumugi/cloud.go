package main

// クラウドのセッション（claude.ai/code）を、紡の所属の中の 1 つのマシン「クラウド」として扱う（2026-11-24 鈴木さん）。
//
//   所属.json のセッションの「マシン」を "クラウド"（または "cloud"）にすると、紡はそのセッションをクラウドで動かす。
//   起こす:   claude --cloud "<最初の指示>" を疑似端末で動かし（term.go）、画面に出た session_… を控える。
//             ★クラウドのセッションを新しく立てるのは対話の画面だけ（-p と組めない・2.1.331）。
//             作業フォルダは git のリポジトリ。GitHub の remote があればそこを、無ければ手元のリポジトリを送る。
//   渡す:     claude -p "<指示>" --cloud <session_…> --output-format json（{ok, session_id, url}）。
//             ★ok は「積んだ」だけで、会話に入った証拠ではない（2026-11-24: 1 回は入ったのを人が画面で確かめた）。
//   見えないもの: クラウドのセッションは claude agents に出ない。動いているか（idle・busy）は紡からは見えない。
//             眠ると VM は数分で止まり、長く放っておくと回収される（会話は戻る・裏の処理は戻らない）。
//   クラウドの中では、結（127.0.0.1）・紡のチャネル・手元の ~/.claude の設定は使えない。claude.ai のコネクタは使える。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func isCloudMachine(m string) bool {
	return m == "クラウド" || strings.EqualFold(m, "cloud")
}

func (s sessionSpec) cloud() bool { return isCloudMachine(s.Machine) }

var cloudIDPattern = regexp.MustCompile(`session_[A-Za-z0-9]{10,}`)

// 新しいクラウドのセッションを立てて、その ID を返す。テストで差し替える。
var cloudCreate = func(dir, prompt string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return "", fmt.Errorf("作業フォルダ %s が git のリポジトリではありません（クラウドはリポジトリを持って起きる）", dir)
	}
	screen, err := runInTerminal(dir, []string{"claude", "--cloud", prompt}, cleanEnv(os.Environ()), cloudCreateWait, func(s string) bool {
		return cloudIDPattern.MatchString(s) && strings.Contains(s, "Created cloud session")
	})
	if id := cloudIDPattern.FindString(screen); id != "" && strings.Contains(screen, "Created cloud session") {
		return id, nil
	}
	if err == nil {
		err = errors.New("画面に session_… が出ませんでした")
	}
	return "", fmt.Errorf("%w。画面の最後: %s", err, tail(screen, 400))
}

// クラウドのセッションを立てるのを待つ長さ（リポジトリを送る・VM を用意するのを含む）。
var cloudCreateWait = 4 * time.Minute

// クラウドのセッションに指示を積む。テストで差し替える。
var cloudSend = func(id, prompt string) error {
	out, err := runClaudeIn("", "-p", prompt, "--cloud", id, "--output-format", "json")
	if err != nil {
		return err
	}
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("答えが読めません: %v: %s", err, tail(string(out), 300))
	}
	if !r.OK {
		return fmt.Errorf("積めませんでした: %s", r.Error)
	}
	return nil
}

// クラウドで起こす最初の指示。紡が付ける前置き＋所属.json の最初の指示。
func cloudPrompt(af affiliation, s sessionSpec, extra string) string {
	p := fmt.Sprintf("【紡】あなたは紡（tsumugi）の所属 %s のセッション「%s」で、クラウド（claude.ai/code）で動いています。"+
		"手元の結（yui）・紡のチャネル・手元の設定は使えません。指示は紡から claude -p --cloud で届きます。\n\n%s", af.ID, s.Name, s.Prompt)
	if extra != "" {
		p += "\n\n" + extra
	}
	return p
}

// クラウドで起こす。控える ID を返す。
func startCloud(w io.Writer, af affiliation, s sessionSpec, extra string) (string, bool) {
	fmt.Fprint(w, "（クラウドで起こす）")
	if s.Trust {
		if err := trustBeforeStart(io.Discard, action{Affiliation: af.ID, Name: s.Name, Dir: s.Dir}, false); err != nil {
			fmt.Fprintln(w, " … 作業フォルダを信頼済みにできず失敗:", err)
			return "", false
		}
	}
	id, err := cloudCreate(s.Dir, cloudPrompt(af, s, extra))
	if err != nil {
		fmt.Fprintln(w, " … 失敗:", err)
		return "", false
	}
	fmt.Fprintf(w, " … した（%s）\n", id)
	return id, true
}

// クラウドのセッションへ渡す（定時・届け）。
func deliverCloud(w io.Writer, d delivery) bool {
	if err := cloudSend(d.SessionID, d.Prompt); err != nil {
		fmt.Fprintln(w, " … クラウドへ積めず失敗:", err)
		return false
	}
	fmt.Fprintln(w, " … した（クラウドへ積んだ。★会話に入ったかは紡からは確かめられない）")
	return true
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return "…" + string(r[len(r)-n:])
	}
	return s
}
