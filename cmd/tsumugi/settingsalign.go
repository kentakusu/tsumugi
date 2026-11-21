package main

// 設定そろえ: Claude Code のユーザー設定（~/.claude/settings.json）を、基本の値に合わせる。
//
//   tsumugi 設定そろえ [--dry]
//
// 基本の値の正は git（このリポジトリの settings/claude-base.json。main の最新を取る）。
// ★既定は「ずれを出すだけ」。書き換えるのは、そのマシンの 紡.json の「設定を合わせる」に
//   載っている鍵だけ（"*" なら基本の値の全部）。受け入れる側が決める（送る側の 1 つの誤りが全マシンに広がらない）。
// ★基本の値に無い鍵（hooks など、環境ごとのもの）には触らない。入れ子は葉の値だけを合わせる。
// 書き換えたら、鍵・前の値・後の値を <手元>/設定そろえの記録.log に残し、元のファイルを .bak-<時刻> で残す。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const baseSettingsURL = "https://raw.githubusercontent.com/kentakusu/tsumugi/main/settings/claude-base.json"

func userSettingsPath() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".claude", "settings.json"), nil
}

func fetchBase() (map[string]any, error) {
	c := http.Client{Timeout: 30 * time.Second}
	resp, err := c.Get(baseSettingsURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("基本の値を取れません: %s", resp.Status)
	}
	var m map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("基本の値を読めません: %w", err)
	}
	return m, nil
}

// 葉の鍵（a.b.c）と値に開く。
func leaves(m map[string]any, prefix string, out map[string]any) {
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok && len(sub) > 0 {
			leaves(sub, prefix+k+".", out)
			continue
		}
		out[prefix+k] = v
	}
}

func jsonStr(v any) string {
	if v == nil {
		return "（無い）"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// ずれを出し、許されたものは合わせる。合わせた数を返す。
func alignSettings(w io.Writer, base map[string]any, path string, allowed []string, dry bool, root string) (int, error) {
	// ★symlink なら、リンクの先に書く（rename で置き換えると symlink が普通のファイルになり、
	// リンク先の git 管理のファイルから黙って切り離される。2026-11-21 Reatex で踏んだ）。
	if r, err := filepath.EvalSymlinks(path); err == nil {
		path = r
	}
	local := newOobj()
	if b, err := os.ReadFile(path); err == nil {
		o, err := parseOrdered(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")))
		if err != nil {
			return 0, fmt.Errorf("%s を読めません（直さずに止めます）: %w", path, err)
		}
		local = o
	}
	bl, ll := map[string]any{}, map[string]any{}
	leaves(base, "", bl)
	leaves(local.plain(), "", ll)
	all := len(allowed) == 1 && allowed[0] == "*"
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	var keys []string
	for k := range bl {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var log strings.Builder
	changed, differ := 0, 0
	for _, k := range keys {
		if jsonStr(ll[k]) == jsonStr(bl[k]) {
			continue
		}
		differ++
		top := strings.SplitN(k, ".", 2)[0]
		if !(all || ok[k] || ok[top]) || dry {
			fmt.Fprintf(w, "ずれ: %s  いま %s ／ 基本 %s\n", k, jsonStr(ll[k]), jsonStr(bl[k]))
			continue
		}
		fmt.Fprintf(w, "合わせる: %s  %s → %s\n", k, jsonStr(ll[k]), jsonStr(bl[k]))
		fmt.Fprintf(&log, "%s %s %s: %s → %s\n", time.Now().Format(time.RFC3339), thisMachine(), k, jsonStr(ll[k]), jsonStr(bl[k]))
		local.setLeaf(strings.Split(k, "."), bl[k])
		changed++
	}
	if differ == 0 {
		fmt.Fprintln(w, "Claude の設定は基本の値とそろっています:", path)
	}
	if changed == 0 {
		return 0, nil
	}
	raw, err := json.Marshal(local)
	if err != nil {
		return 0, err
	}
	var pretty bytes.Buffer
	json.Indent(&pretty, raw, "", "  ")
	// 控えは紡の置き場に置く（リポジトリの中に置くと、自動同期で commit される）。
	if old, err := os.ReadFile(path); err == nil {
		d := filepath.Join(root, "設定の控え")
		os.MkdirAll(d, 0o700)
		os.WriteFile(filepath.Join(d, "settings.json.bak-"+time.Now().Format("20060102150405")), old, 0o600)
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(pretty.Bytes(), '\n'), mode); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err
	}
	if f, err := os.OpenFile(filepath.Join(root, "設定そろえの記録.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		f.WriteString(log.String())
		f.Close()
	}
	return changed, nil
}

func cmdSettingsAlign(args []string) error {
	dry := len(args) > 0 && args[0] == "--dry"
	if len(args) > 1 || (len(args) == 1 && !dry) {
		return fmt.Errorf("使い方: tsumugi 設定そろえ [--dry]")
	}
	root, err := homeDir()
	if err != nil {
		return err
	}
	g, err := loadGlobal(root)
	if err != nil {
		return err
	}
	base, err := fetchBase()
	if err != nil {
		return err
	}
	p, err := userSettingsPath()
	if err != nil {
		return err
	}
	if len(g.AlignSettings) == 0 && !dry {
		fmt.Printf("（%s の「設定を合わせる」が無いので、ずれを出すだけ）\n", filepath.Join(root, globalFile))
	}
	_, err = alignSettings(os.Stdout, base, p, g.AlignSettings, dry, root)
	return err
}
