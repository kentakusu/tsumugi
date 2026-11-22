package main

// 作業フォルダを先に信頼済みにする（所属.json のセッションの「信頼する」）。
//
// Claude Code は、信頼していないフォルダでは --bg のセッションを起こさない（Workspace not trusted）。
// 信頼の確認は対話なので、新しい作業フォルダは紡が無人で起こせない。
// 「信頼する」: true と書いたセッションだけ、紡が起こす直前に ~/.claude.json の
// projects.<作業フォルダ>.hasTrustDialogAccepted を true にする（書いた人の判断で信頼する形）。
//
// ★~/.claude.json は Claude Code が動きながら書き換える。書く直前に読み直し、触るのはその 1 か所だけ。
//   書く前の中身を <手元>/設定の控え/ に残し、書いたら読み戻して、その 1 か所のほかが変わっていないことを確かめる。
// ★読めない（壊れた）~/.claude.json には書かずに止める。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"
)

const trustLeaf = "hasTrustDialogAccepted"

// Claude Code のグローバル設定（~/.claude.json）の場所。CLAUDE_CONFIG_DIR があればその中。
func claudeConfigPath() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json"), nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".claude.json"), nil
}

// projects の鍵の形。
//   - Linux・macOS: 絶対パス（UseAI の ~/.claude.json で確かめた: "/home/…"）
//   - Windows: 区切りを "/" にした形（"D:/Users/kenta/…"）。Windows の ~/.claude.json で、
//     Claude Code が自分で書いた項（hasTrustDialogAccepted:false のもの）がこの形だった（2026-11-22 WSL から読んだ）。
//     ★Windows の実機で信頼が効くかは tsumugi-windows に確かめてもらう。
func trustKey(dir, goos string) string {
	if goos == "windows" {
		return strings.ReplaceAll(dir, `\`, "/")
	}
	return dir
}

// 既に在る鍵のうち、同じフォルダを指すもの（Windows は大文字小文字を区別しない）。無ければ want。
func matchKey(projects *oobj, want, goos string) string {
	if projects == nil {
		return want
	}
	if _, ok := projects.vals[want]; ok {
		return want
	}
	if goos == "windows" {
		for _, k := range projects.keys {
			// "\" の形の鍵は Claude Code が読む形ではないので選ばない（そこに書いても効かない）
			if !strings.Contains(k, `\`) && strings.EqualFold(k, want) {
				return k
			}
		}
	}
	return want
}

// 作業フォルダを絶対パスにして確かめる（在るフォルダだけ）。
func trustDir(dir, goos string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("作業フォルダ %s が在りません: %w", abs, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("作業フォルダ %s はフォルダではありません", abs)
	}
	// Linux・macOS の claude は実体のパス（getcwd）で見るので、symlink は解く。
	// Windows ではジャンクションなどで形が変わるのを避けて解かない。
	if goos != "windows" {
		if r, err := filepath.EvalSymlinks(abs); err == nil {
			abs = r
		}
	}
	return abs, nil
}

// 信頼の印を書く。書いたら true。既に信頼済みなら書かずに false。
func trustFolder(cfgPath, dir, backupRoot string) (bool, error) {
	return trustFolderOn(runtime.GOOS, cfgPath, dir, backupRoot)
}

func trustFolderOn(goos, cfgPath, dir, backupRoot string) (bool, error) {
	abs, err := trustDir(dir, goos)
	if err != nil {
		return false, err
	}
	// symlink なら、リンクの先に書く（rename で置き換えると symlink が普通のファイルになる）。
	if r, err := filepath.EvalSymlinks(cfgPath); err == nil {
		cfgPath = r
	}
	// Claude Code が間に書き換えたら、読み直してやり直す（数回まで）。
	for try := 0; try < 3; try++ {
		done, wrote, err := trustOnce(goos, cfgPath, abs, backupRoot)
		if done || err != nil {
			return wrote, err
		}
	}
	return false, fmt.Errorf("%s が書いている間に何度も変わったので、書かずに止めます", cfgPath)
}

var errConfigBroken = errors.New("読めないので書かずに止めます")

// 1 回ぶん。done=false はやり直し（書く直前に中身が変わった）。
func trustOnce(goos, cfgPath, abs, backupRoot string) (done, wrote bool, err error) {
	before, err := os.ReadFile(cfgPath)
	if errors.Is(err, fs.ErrNotExist) {
		return true, false, fmt.Errorf("%s が在りません（Claude Code を一度も開いていない？）。作らずに止めます", cfgPath)
	}
	if err != nil {
		return true, false, err
	}
	cfg, err := parseOrdered(bytes.TrimPrefix(before, []byte("\xef\xbb\xbf")))
	if err != nil {
		return true, false, fmt.Errorf("%s: %w: %v", cfgPath, errConfigBroken, err)
	}
	var projects *oobj
	if v, ok := cfg.vals["projects"]; ok {
		if projects, ok = v.(*oobj); !ok {
			return true, false, fmt.Errorf("%s: projects がオブジェクトではありません。%w", cfgPath, errConfigBroken)
		}
	}
	key := matchKey(projects, trustKey(abs, goos), goos)
	if projects != nil {
		if v, ok := projects.vals[key]; ok {
			p, ok := v.(*oobj)
			if !ok {
				return true, false, fmt.Errorf("%s: projects[%q] がオブジェクトではありません。%w", cfgPath, key, errConfigBroken)
			}
			if b, _ := p.vals[trustLeaf].(bool); b {
				return true, false, nil // 既に信頼済み
			}
		}
	}
	path := []string{"projects", key, trustLeaf}
	want := cfg.plain()
	setPlainLeaf(want, path, true)
	cfg.setLeaf(path, true)

	raw, err := json.Marshal(cfg)
	if err != nil {
		return true, false, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return true, false, err
	}
	out.WriteByte('\n')

	mode := os.FileMode(0o600)
	if fi, err := os.Stat(cfgPath); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := fmt.Sprintf("%s.tsumugi-tmp.%d", cfgPath, os.Getpid())
	if err := os.WriteFile(tmp, out.Bytes(), mode); err != nil {
		return true, false, err
	}
	// 書く直前に読み直す。読んだ時から変わっていたら、作り直す。
	if now, err := os.ReadFile(cfgPath); err != nil || !bytes.Equal(now, before) {
		os.Remove(tmp)
		if err != nil {
			return true, false, err
		}
		return false, false, nil
	}
	// 控えは紡の置き場に置く。本人だけが読める権限で（~/.claude.json にはアカウントの情報が入っている）。
	d := filepath.Join(backupRoot, "設定の控え")
	if err := os.MkdirAll(d, 0o700); err != nil {
		os.Remove(tmp)
		return true, false, fmt.Errorf("控えを置けないので書きません: %w", err)
	}
	bak := filepath.Join(d, ".claude.json.bak-"+time.Now().Format("20060102150405.000"))
	if err := os.WriteFile(bak, before, 0o600); err != nil {
		os.Remove(tmp)
		return true, false, fmt.Errorf("控えを置けないので書きません: %w", err)
	}
	if err := os.Rename(tmp, cfgPath); err != nil {
		os.Remove(tmp)
		return true, false, err
	}
	// 読み戻して確かめる: その 1 か所が true で、ほかは書く前と同じ。
	back, err := os.ReadFile(cfgPath)
	if err != nil {
		return true, true, fmt.Errorf("書いたあと読み戻せません（控え: %s）: %w", bak, err)
	}
	got, err := parseOrdered(bytes.TrimPrefix(back, []byte("\xef\xbb\xbf")))
	if err != nil {
		return true, true, fmt.Errorf("書いたあと読めません（控え: %s）: %w", bak, err)
	}
	if !reflect.DeepEqual(got.plain(), want) {
		return true, true, fmt.Errorf("書いたあと、信頼の印のほかにも違いがあります（Claude Code が同時に書いた？）。控え: %s", bak)
	}
	return true, true, nil
}

// 普通の map の a.b.c に値を置く（読み戻しと比べる「あるべき姿」を作るため）。
func setPlainLeaf(m map[string]any, path []string, v any) {
	cur := m
	for _, p := range path[:len(path)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[path[len(path)-1]] = v
}
