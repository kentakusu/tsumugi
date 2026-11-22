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
	"strconv"
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
	// 動かすマシン（ホスト名）。空ならどのマシンでも。合わないマシンの紡は起こさず・止めない。
	Machine string `json:"マシン,omitempty"`
	// 決まった時刻に届ける指示。
	Schedules []scheduleSpec `json:"定時,omitempty"`
}

// このマシンの名前。決め方は次の順:
//  1. 環境変数 TSUMUGI_MACHINE
//  2. <手元>/紡.json の「マシン」
//  3. WSL なら「ホスト名/ディストロ名」（WSL_DISTRO_NAME）、ほかはホスト名
//
// ★WSL_DISTRO_NAME は、systemd の timer や claude の背景セッションには渡らない（実測 2026-11-17）。
// そこで 3 だけに頼ると、手で叩いたときと自動のときで名前が変わる（「よそのマシン」扱いで
// 起こさない・定時を届けない、結の書き手が割れる）。WSL では 紡.json に「マシン」を書いておく。
// ★結の帰属（名乗り）にも入るので、形を変えると別の書き手になる。変えない。
func thisMachine() string {
	if m := os.Getenv("TSUMUGI_MACHINE"); m != "" {
		return m
	}
	if root, err := homeDir(); err == nil {
		if g, err := loadGlobal(root); err == nil && g.Machine != "" {
			return g.Machine
		}
	}
	return machineFromEnv()
}

func machineFromEnv() string {
	h, _ := os.Hostname()
	if d := os.Getenv("WSL_DISTRO_NAME"); d != "" {
		return h + "/" + d
	}
	return h
}

// WSL の上で動いていて、マシンの名前が決め打ちされていないか（run が知らせるため）。
func machineUnpinnedOnWSL() bool {
	if os.Getenv("TSUMUGI_MACHINE") != "" {
		return false
	}
	if root, err := homeDir(); err == nil {
		if g, err := loadGlobal(root); err == nil && g.Machine != "" {
			return false
		}
	}
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// このマシンで動かすものか。
func (s sessionSpec) here(machine string) bool {
	return s.Machine == "" || strings.EqualFold(s.Machine, machine)
}

// 所属.json の中身。
type affiliation struct {
	ID       string        `json:"-"` // フォルダ名
	Path     string        `json:"-"` // 所属.json の場所
	only     bool          // 一部のセッションだけを扱う（止める・控えを書き戻す対象を絞る）
	Label    string        `json:"表示名"`
	Sessions []sessionSpec `json:"セッション"`
	Watch    watchSpec     `json:"見張り,omitempty"`
	// この所属が使える結の層と、既定の書き先。yui.go
	Yui *affYui `json:"結,omitempty"`
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
		if _, err := strconv.Atoi(s.Name); err == nil {
			return a, fmt.Errorf("%s: セッション名「%s」は数字だけなので使えません（tsumugi a <番号> と区別できない）", p, s.Name)
		}
		if seen[s.Name] {
			return a, fmt.Errorf("%s: セッション名「%s」が重複しています", p, s.Name)
		}
		seen[s.Name] = true
		for j, sc := range s.Schedules {
			if err := sc.validate(); err != nil {
				return a, fmt.Errorf("%s: 「%s」の %d 番目の定時: %w", p, s.Name, j+1, err)
			}
		}
		a.Sessions[i].Dir = expandHome(s.Dir)
	}
	return a, nil
}
