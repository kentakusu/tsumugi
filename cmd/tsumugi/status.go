package main

// 望む状態（所属.json）と実際（claude agents）を並べる。

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
)

type row struct {
	Affiliation string // 所属。所属に無いものは空
	Name        string
	Want        bool   // 所属.json に在るか
	State       string // 実際の状態。動いていなければ空
	Kind        string
	Dir         string
	OnDemand    bool // 常駐しない（止まっていて当たり前）
}

// 突き合わせる。セッション名で対応を取る。
func compare(affs []affiliation, as []agent) []row {
	running := map[string]agent{}
	for _, a := range as {
		running[a.Name] = a
	}
	var rows []row
	claimed := map[string]bool{}
	for _, af := range affs {
		for _, s := range af.Sessions {
			r := row{Affiliation: af.ID, Name: s.Name, Want: true, Dir: s.Dir, OnDemand: !s.resident()}
			if a, ok := running[s.Name]; ok {
				r.State, r.Kind = a.Status, a.Kind
				claimed[s.Name] = true
			}
			rows = append(rows, r)
		}
	}
	var stray []row
	for _, a := range as {
		if claimed[a.Name] {
			continue
		}
		stray = append(stray, row{Name: a.Name, State: a.Status, Kind: a.Kind, Dir: a.Cwd})
	}
	sort.Slice(stray, func(i, j int) bool { return stray[i].Name < stray[j].Name })
	return append(rows, stray...)
}

func printStatus(w io.Writer, rows []row) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "所属\tセッション\t望む\t実際\t種類\t作業フォルダ")
	for _, r := range rows {
		aff, want, state, kind := r.Affiliation, "要る", r.State, r.Kind
		if aff == "" {
			aff, want = "（所属なし）", "-"
		}
		if state == "" && r.OnDemand {
			state, kind = "止まっている（必要なときに起こす）", "-"
		} else if state == "" {
			state, kind = "★動いていない", "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", aff, r.Name, want, state, kind, r.Dir)
	}
	tw.Flush()
}
