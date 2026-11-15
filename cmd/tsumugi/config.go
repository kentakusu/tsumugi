package main

// 所属の設定を読む。
//
// 置き場所は <手元>/<所属>/所属.json。<手元> は環境変数 TSUMUGI_HOME、
// 無ければ ~/.tsumugi。所属の名前はフォルダ名そのもの。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 所属.json の 1 本ぶんのセッション。
type sessionSpec struct {
	Name    string `json:"名前"`
	Dir     string `json:"作業フォルダ"`
	Prompt  string `json:"最初の指示"`
	Model   string `json:"モデル,omitempty"`
	Comment string `json:"メモ,omitempty"`
	// 権限モード（claude --permission-mode）。無人なので決めておく。
	Permission string `json:"権限モード,omitempty"`
	// 既存の会話（セッション ID）を写して始める。ほかの場所で始めた会話を
	// 紡に移すときに使う。紡が一度起こしたあとは見ない（状態.json が優先）。
	From string `json:"引き継ぐ会話,omitempty"`
}

// 所属.json の中身。
type affiliation struct {
	ID       string        `json:"-"` // フォルダ名
	Path     string        `json:"-"` // 所属.json の場所
	Label    string        `json:"表示名"`
	Sessions []sessionSpec `json:"セッション"`
}

const configFile = "所属.json"

// 手元の置き場所。
func homeDir() (string, error) {
	if h := os.Getenv("TSUMUGI_HOME"); h != "" {
		return h, nil
	}
	u, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ホームフォルダが分かりません: %w", err)
	}
	return filepath.Join(u, ".tsumugi"), nil
}

// "~/" で始まるパスをホームフォルダに置き換える。
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if u, err := os.UserHomeDir(); err == nil {
			return filepath.Join(u, p[1:])
		}
	}
	return p
}

// 全部の所属を読む。所属.json が無いフォルダは所属とみなさない。
func loadAffiliations(root string) ([]affiliation, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s を読めません: %w", root, err)
	}
	var out []affiliation
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name(), configFile)
		a, err := loadAffiliation(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		a.ID = e.Name()
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func loadAffiliation(p string) (affiliation, error) {
	var a affiliation
	b, err := os.ReadFile(p)
	if err != nil {
		return a, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields() // 書き間違えたキーを黙って捨てない
	if err := dec.Decode(&a); err != nil {
		return a, fmt.Errorf("%s の書き方が違います: %w", p, err)
	}
	a.Path = p
	seen := map[string]bool{}
	for i, s := range a.Sessions {
		if s.Name == "" {
			return a, fmt.Errorf("%s: %d 番目のセッションに「名前」がありません", p, i+1)
		}
		if seen[s.Name] {
			return a, fmt.Errorf("%s: セッション名「%s」が重複しています", p, s.Name)
		}
		seen[s.Name] = true
		a.Sessions[i].Dir = expandHome(s.Dir)
	}
	return a, nil
}
