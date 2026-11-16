package main

// 設定項目: Claude Code の settings.json に書ける項目の一覧を取り、前回からの増減と、
// 手元の設定に「一覧に無い鍵」が無いかを見る。
//
// 一覧の出どころは SchemaStore の JSON Schema（公式ドキュメントからの写しで、少し遅れることがある）。
// ★一覧に無い鍵は「誤り」とは限らない（新しすぎる・隠し設定）。消さずに、人と追従のセッションが判断する。

import (
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

const defaultSchemaURL = "https://www.schemastore.org/claude-code-settings.json"
const settingsSnapshot = "設定項目.json"

type schemaSnap struct {
	Taken string            `json:"取った"`
	From  string            `json:"出どころ"`
	Keys  map[string]string `json:"項目"` // 鍵 → 説明の 1 行目
}

func fetchSchema(url string) (schemaSnap, error) {
	snap := schemaSnap{From: url, Taken: time.Now().Format(time.RFC3339), Keys: map[string]string{}}
	c := http.Client{Timeout: 30 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return snap, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return snap, fmt.Errorf("%s が %s を返しました", url, resp.Status)
	}
	var s struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&s); err != nil {
		return snap, fmt.Errorf("設定項目の一覧を読めません: %w", err)
	}
	for k, v := range s.Properties {
		d, _, _ := strings.Cut(v.Description, "\n")
		snap.Keys[k] = d
	}
	if len(snap.Keys) == 0 {
		return snap, fmt.Errorf("設定項目が 0 件でした（出どころの形が変わった？）")
	}
	return snap, nil
}

// 増減を文にする（変わっていなければ空）。
func diffSnap(prev, cur schemaSnap) string {
	var add, del []string
	for k, d := range cur.Keys {
		if _, ok := prev.Keys[k]; !ok {
			add = append(add, "  + "+k+"  "+d)
		}
	}
	for k := range prev.Keys {
		if _, ok := cur.Keys[k]; !ok {
			del = append(del, "  - "+k)
		}
	}
	if len(add)+len(del) == 0 || len(prev.Keys) == 0 {
		return ""
	}
	sort.Strings(add)
	sort.Strings(del)
	return fmt.Sprintf("Claude Code の設定項目が変わりました（%d → %d）\n%s", len(prev.Keys), len(cur.Keys), strings.Join(append(add, del...), "\n"))
}

// 手元の設定ファイルのうち、一覧に無い鍵。
func unknownKeys(paths []string, snap schemaSnap) []string {
	var out []string
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) != nil {
			out = append(out, p+": JSON として読めません")
			continue
		}
		var ks []string
		for k := range m {
			if _, ok := snap.Keys[k]; !ok {
				ks = append(ks, k)
			}
		}
		sort.Strings(ks)
		if len(ks) > 0 {
			out = append(out, p+": "+strings.Join(ks, ", "))
		}
	}
	return out
}

// 見る設定ファイル: ユーザーの settings.json と、紡のセッションの作業フォルダの .claude/settings*.json。
func settingsFiles(root string) []string {
	var ps []string
	if h, err := os.UserHomeDir(); err == nil {
		ps = append(ps, filepath.Join(h, ".claude", "settings.json"))
	}
	affs, _ := loadAffiliations(root)
	seen := map[string]bool{}
	for _, af := range affs {
		for _, s := range af.Sessions {
			d := expandHome(s.Dir)
			if seen[d] {
				continue
			}
			seen[d] = true
			ps = append(ps, filepath.Join(d, ".claude", "settings.json"), filepath.Join(d, ".claude", "settings.local.json"))
		}
	}
	return ps
}

func checkSettings(w io.Writer, root, url string, dry bool) (string, error) {
	if url == "" {
		url = defaultSchemaURL
	}
	cur, err := fetchSchema(url)
	if err != nil {
		return "", err
	}
	var prev schemaSnap
	p := filepath.Join(root, settingsSnapshot)
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &prev)
	}
	fmt.Fprintf(w, "設定項目: %d 件（%s）\n", len(cur.Keys), url)
	diff := diffSnap(prev, cur)
	if diff != "" {
		fmt.Fprintln(w, diff)
	}
	if unk := unknownKeys(settingsFiles(root), cur); len(unk) > 0 {
		fmt.Fprintln(w, "一覧に無い鍵（新しすぎる・隠し設定・書き誤りのどれか。消さずに確かめる）:")
		for _, u := range unk {
			fmt.Fprintln(w, "  "+u)
		}
	}
	if !dry {
		b, _ := json.MarshalIndent(cur, "", "  ")
		if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
			return diff, err
		}
	}
	return diff, nil
}

func cmdSettings(dry bool) error {
	root, err := homeDir()
	if err != nil {
		return err
	}
	g, err := loadGlobal(root)
	if err != nil {
		return err
	}
	url := ""
	if g.Update != nil {
		url = g.Update.SchemaURL
	}
	_, err = checkSettings(os.Stdout, root, url, dry)
	return err
}
