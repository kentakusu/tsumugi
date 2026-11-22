package main

// claude agents と紡を揃える（2026-11-22 鈴木さん）:
//   - claude agents に出るものは、紡と関係の無いものも含めて全部 tsumugi l・tsumugi tree に出す
//   - 紡が過去に管理していた会話（控えの「前の会話」・同じ名前の古い会話）が Completed に残っていれば claude rm で片付ける
//   - 所属.json に無い名前で手で起こした背景セッションは、その機の所属に「常駐しない」で足して配下に入れる
//   - tsumugi tree: セッションごとに、いまの会話と前の会話を木の形で並べる
//   - tsumugi 新しく <名前>: 会話が長くなりすぎたときなどに、同じ名前のまま新しい会話で最初の指示から始め直す

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// 紡の控え（いまの会話・前の会話）のどれかに当たる会話 ID → (所属, セッション名, いまか)。
type owner struct {
	aff, name string
	current   bool
}

func owners(states map[string]affState) map[string]owner {
	m := map[string]owner{}
	for aff, st := range states {
		for name, ss := range st.Sessions {
			for _, p := range ss.Previous {
				m[p.ID] = owner{aff, name, false}
			}
			if ss.SessionID != "" {
				m[ss.SessionID] = owner{aff, name, true}
			}
		}
	}
	return m
}

// claude agents の Completed（動いていないもの）のうち、紡が過去に管理していたものを claude rm で片付ける。
//   - 控えの「前の会話」に在る ID
//   - 所属.json の名前と同じ名前で、いまの控えの会話ではないもの（同じ名前の古い会話）
//
// いまの控えの会話（常駐しないものが止まっているときなど）と、紡と関係の無いものには触らない。
// 片付けたものは「前の会話」に残す（tsumugi tree に出る）。
func cleanupCompleted(w io.Writer, machine string, affs []affiliation, states map[string]affState, all []agent, dry bool) bool {
	own := owners(states)
	names := map[string]string{} // セッション名 → 所属
	for _, af := range affs {
		for _, s := range af.Sessions {
			if s.here(machine) {
				names[s.Name] = af.ID
			}
		}
	}
	changed := false
	for _, a := range all {
		if a.Pid > 0 || a.SessionID == "" || a.ID == "" {
			continue
		}
		o, known := own[a.SessionID]
		if known && o.current {
			continue // いまの控えの会話（止まっている常駐しないもの等）は残す
		}
		aff, name := o.aff, o.name
		if !known {
			if af, ok := names[a.Name]; ok {
				aff, name = af, a.Name // 同じ名前の古い会話
			} else {
				continue // 紡と関係の無いもの
			}
		}
		fmt.Fprintf(w, "%s/%s: 前の会話 %s（%s）を claude の一覧から外す", aff, name, short(a.SessionID), agentLabel(a))
		if dry {
			fmt.Fprintln(w, "（しない）")
			continue
		}
		if _, err := runClaude("rm", a.ID); err != nil && !strings.Contains(err.Error(), "No session matching") {
			fmt.Fprintln(w, " … 外せず:", err)
			continue
		}
		fmt.Fprintln(w, " … した（会話の記録は残る）")
		if !known {
			st := states[aff]
			ss := st.Sessions[name]
			ss.Previous = append(ss.Previous, prevSession{ID: a.SessionID, Until: time.UnixMilli(a.StartedAt).Format(time.RFC3339), Why: "同じ名前の古い会話"})
			st.Sessions[name] = ss
			states[aff] = st
		}
		changed = true
	}
	return changed
}

// 所属.json に無い名前で動いている背景セッション（人が claude で手で起こしたもの）を、その機の所属に「常駐しない」で足す。
// 足したものは、次の adoptRunning が空いているときに同じ会話 ID のまま紡の設定で付け直す。
// 画面で開いているもの（interactive）は足さない（人が打ち込んでいる。一覧には出す）。
// どの所属に足すか決められない（その機に所属が複数あり、作業フォルダでも決まらない）ときは★で知らせて足さない。
func adoptUnknown(w io.Writer, machine string, affs []affiliation, states map[string]affState, all []agent, dry bool) bool {
	own := owners(states)
	taken := map[string]bool{}
	for _, af := range affs {
		for _, s := range af.Sessions {
			taken[s.Name] = true
		}
	}
	changed := false
	for _, a := range all {
		if a.Pid <= 0 || a.Kind == "interactive" || a.SessionID == "" {
			continue
		}
		if _, ok := own[a.SessionID]; ok || taken[a.Name] {
			continue // 紡の控えに在る・所属.json の名前（adoptRunning が扱う）
		}
		if !hasTranscript(a.SessionID) {
			continue // 中身の無い会話は足さない（Claude Code が起こした空のセッション）
		}
		idx := pickAffiliation(machine, affs, a.Cwd)
		if idx < 0 {
			fmt.Fprintf(w, "★紡の外で動いている %s（%s・%s）を足す所属を決められません（この機に所属が複数あり、作業フォルダでも決まらない）。所属.json に足してください\n", short(a.SessionID), a.Name, a.Cwd)
			continue
		}
		af := &affs[idx]
		name := a.Name
		if name == "" || taken[name] {
			name = "手で起こした-" + short(a.SessionID)
		}
		fmt.Fprintf(w, "%s/%s: 紡の外で動いている %s を、常駐しないセッションとして所属.json に足して配下に入れる", af.ID, name, short(a.SessionID))
		if dry {
			fmt.Fprintln(w, "（しない）")
			continue
		}
		if err := appendSession(af.Path, name, a.Cwd, machine); err != nil {
			fmt.Fprintln(w, " … 所属.json に書けず:", err)
			continue
		}
		fmt.Fprintln(w, " … した")
		no := false
		af.Sessions = append(af.Sessions, sessionSpec{Name: name, Dir: a.Cwd, Resident: &no, Machine: machine})
		st := states[af.ID]
		if st.Sessions == nil {
			st.Sessions = map[string]sessionState{}
		}
		st.Sessions[name] = sessionState{SessionID: a.SessionID}
		states[af.ID] = st
		taken[name] = true
		changed = true
	}
	return changed
}

// 手で起こしたものを足す所属。その機の所属が 1 つならそれ。複数なら、作業フォルダがいちばん長く重なる所属。決まらなければ -1。
func pickAffiliation(machine string, affs []affiliation, cwd string) int {
	var here []int
	for i, af := range affs {
		for _, s := range af.Sessions {
			if s.here(machine) {
				here = append(here, i)
				break
			}
		}
	}
	if len(here) == 1 {
		return here[0]
	}
	best, bestLen, tie := -1, 0, false
	c := strings.ToLower(filepath.ToSlash(cwd))
	for _, i := range here {
		for _, s := range affs[i].Sessions {
			d := strings.ToLower(strings.TrimSuffix(filepath.ToSlash(expandHome(s.Dir)), "/"))
			if d == "" || !(c == d || strings.HasPrefix(c, d+"/")) {
				continue
			}
			if len(d) > bestLen {
				best, bestLen, tie = i, len(d), false
			} else if len(d) == bestLen && best != i {
				tie = true
			}
		}
	}
	if tie {
		return -1
	}
	return best
}

// 所属.json の「セッション」に 1 項を足す（並びと、ほかの鍵はそのまま）。
func appendSession(path, name, dir, machine string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	doc, err := parseOrdered(raw)
	if err != nil {
		return err
	}
	item := newOobj()
	item.set("名前", name)
	item.set("作業フォルダ", dir)
	item.set("常駐", false)
	item.set("マシン", machine)
	item.set("メモ", "紡の外で動いていたものを "+time.Now().Format("2006-01-02 15:04")+" に紡が足した（手で起こしたもの）")
	list, _ := doc.vals["セッション"].([]any)
	doc.set("セッション", append(list, item))
	return writeOrdered(path, doc)
}

// tsumugi l（2026-11-22 鈴木さん「claude agents と一覧は必ず同じに。紡を経由しなかったとしても」）:
//
//	上の段: claude agents の行を、同じ並び・同じ ID・同じ名前・同じ様子で全部出し、紡では何か（所属/セッション・前の会話・紡の外）を添える。
//	        毎回 claude agents から作るので、claude を直接打った変更（claude rm・claude stop 等）もそのまま映る。
//	下の段: 所属.json にあるのに claude agents に出ていないセッション（まだ起こしていない・claude rm で外された・よそのマシン）。
func printListFull(w io.Writer, machine string, affs []affiliation, states map[string]affState, all []agent) {
	type ref struct {
		no           int
		aff, name    string
		held         bool
		resident     bool
		here         bool
		machine, dir string
		cur          string
	}
	var refs []ref
	n := 0
	for _, af := range affs {
		for _, s := range af.Sessions {
			n++
			ss := states[af.ID].Sessions[s.Name]
			refs = append(refs, ref{n, af.ID, s.Name, ss.Held, s.resident(), s.here(machine), s.Machine, s.Dir, ss.SessionID})
		}
	}
	// 会話 ID → 紡のセッション（いまの会話）。控えが無いセッションは、同じ名前で動いているもの（無ければいちばん新しいもの）を当てる
	current := map[string]int{}
	for i, r := range refs {
		if r.cur != "" {
			current[r.cur] = i
			continue
		}
		if !r.here {
			continue
		}
		if a, ok := newestRunning(all, r.name); ok {
			current[a.SessionID] = i
		} else if a, ok := newest(all, r.name); ok {
			current[a.SessionID] = i
		}
	}
	own := owners(states)
	seen := map[int]bool{}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "番号\tClaude での名前\t実際\tID\t紡では\t作業フォルダ")
	for _, a := range all {
		if a.SessionID == "" {
			continue
		}
		no, role := "-", "（紡の外）"
		if i, ok := current[a.SessionID]; ok {
			r := refs[i]
			seen[i] = true
			no, role = fmt.Sprint(r.no), r.aff+"/"+r.name
			if a.Name != r.name {
				role += " ★名前が違う"
			}
		} else if o, ok := own[a.SessionID]; ok {
			role = o.aff + "/" + o.name + "（前の会話）"
		}
		state := agentLabel(a)
		if a.Kind == "interactive" {
			state += "・画面"
		}
		if i, ok := current[a.SessionID]; ok && refs[i].held && a.Pid <= 0 {
			state += "・止めてある（tsumugi a で起こす）"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", no, quoteName(a.Name), state, a.ID, role, a.Cwd)
	}
	tw.Flush()
	var missing []ref
	for i, r := range refs {
		if !seen[i] {
			missing = append(missing, r)
		}
	}
	if len(missing) == 0 {
		return
	}
	fmt.Fprintln(w, "\n（claude の一覧に無い紡のセッション）")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "番号\t名前\t所属\t実際\t控えの ID\tマシン\t作業フォルダ")
	for _, r := range missing {
		state, m, id := "まだ起こしていない", machine, "-"
		if r.cur != "" {
			state, id = "claude の一覧に無い（tsumugi a で起こす）", short(r.cur)
		}
		if !r.resident && r.cur == "" {
			state = "止まっている（必要なときに起こす）"
		}
		if r.held {
			state = "止めてある（tsumugi a で起こす）"
		}
		if !r.here {
			state, m = "（よそのマシン）", r.machine
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", r.no, r.name, r.aff, state, id, m, r.dir)
	}
	tw.Flush()
}

func agentByID(all []agent, id string) (agent, bool) {
	if id == "" {
		return agent{}, false
	}
	var best agent
	found := false
	for _, a := range all {
		if a.SessionID == id && (!found || a.Pid > 0) {
			best, found = a, true
		}
	}
	return best, found
}

// tsumugi tree: セッションごとに、いまの会話と前の会話を木の形で並べる。紡の外のものは作業フォルダごとにまとめる。
func printTree(w io.Writer, machine string, affs []affiliation, states map[string]affState, all []agent) {
	byID := map[string]agent{}
	for _, a := range all {
		if a.SessionID != "" {
			if b, ok := byID[a.SessionID]; !ok || a.Pid > 0 || b.Pid == 0 {
				byID[a.SessionID] = a
			}
		}
	}
	used := map[string]bool{}
	desc := func(id string) string {
		a, ok := byID[id]
		if !ok {
			return "一覧に無い（会話の記録は残る）"
		}
		used[id] = true
		s := agentLabel(a) + "・Claude での名前 " + quoteName(a.Name)
		if a.StartedAt > 0 {
			s += "・起きた " + time.UnixMilli(a.StartedAt).Format("01-02 15:04")
		}
		return s
	}
	for _, af := range affs {
		fmt.Fprintf(w, "%s（%s）\n", af.ID, af.Label)
		for i, s := range af.Sessions {
			last := i == len(af.Sessions)-1
			br, pad := "├─ ", "│   "
			if last {
				br, pad = "└─ ", "    "
			}
			kind := "常駐"
			if !s.resident() {
				kind = "常駐しない"
			}
			if states[af.ID].Sessions[s.Name].Held {
				kind += "・止めてある（tsumugi a で起こす）"
			}
			fmt.Fprintf(w, "%s%s  [%s・%s]  %s\n", br, s.Name, kind, s.machineLabel(machine), s.Dir)
			ss := states[af.ID].Sessions[s.Name]
			nowBr := "├─"
			if len(ss.Previous) == 0 {
				nowBr = "└─"
			}
			if ss.SessionID != "" {
				fmt.Fprintf(w, "%s%s いま  %s  %s\n", pad, nowBr, short(ss.SessionID), desc(ss.SessionID))
			} else {
				fmt.Fprintf(w, "%s%s いま  （まだ起こしていない）\n", pad, nowBr)
			}
			for j := len(ss.Previous) - 1; j >= 0; j-- {
				p := ss.Previous[j]
				br2 := "├─"
				if j == 0 {
					br2 = "└─"
				}
				when := p.Until
				if t, err := time.Parse(time.RFC3339, p.Until); err == nil {
					when = t.Format("01-02 15:04")
				}
				fmt.Fprintf(w, "%s%s 前    %s  %s まで・%s・%s\n", pad, br2, short(p.ID), when, p.Why, desc(p.ID))
			}
		}
	}
	// 紡の外（claude agents に出ていて、紡の控えに無いもの）
	groups := map[string][]agent{}
	var dirs []string
	for _, a := range all {
		if a.SessionID == "" || used[a.SessionID] {
			continue
		}
		if _, ok := groups[a.Cwd]; !ok {
			dirs = append(dirs, a.Cwd)
		}
		groups[a.Cwd] = append(groups[a.Cwd], a)
	}
	if len(dirs) == 0 {
		return
	}
	sort.Strings(dirs)
	fmt.Fprintln(w, "（紡の外・作業フォルダごと）")
	for i, d := range dirs {
		br, pad := "├─ ", "│   "
		if i == len(dirs)-1 {
			br, pad = "└─ ", "    "
		}
		fmt.Fprintf(w, "%s%s\n", br, d)
		as := groups[d]
		sort.Slice(as, func(x, y int) bool { return as[x].StartedAt > as[y].StartedAt })
		for j, a := range as {
			br2 := "├─"
			if j == len(as)-1 {
				br2 = "└─"
			}
			st := agentLabel(a)
			if a.Kind == "interactive" {
				st += "・画面"
			}
			fmt.Fprintf(w, "%s%s %s  %s  %s\n", pad, br2, a.ID, quoteName(a.Name), st)
		}
	}
}

// 一覧に出す様子。動いていれば status（idle・busy 等）、止まっていれば Claude Code の state（stopped・done・failed 等）。
func agentLabel(a agent) string {
	if a.Pid > 0 && a.Status != "" {
		return a.Status
	}
	if a.State != "" {
		return a.State
	}
	if a.Status != "" {
		return a.Status
	}
	return "止まっている"
}

func quoteName(n string) string {
	if n == "" {
		return "（名前なし）"
	}
	return n
}

func (s sessionSpec) machineLabel(machine string) string {
	if s.Machine == "" {
		return "どのマシンでも"
	}
	if s.here(machine) {
		return s.Machine
	}
	return s.Machine + "（よそのマシン）"
}

func cmdTree() error {
	root, err := homeDir()
	if err != nil {
		return err
	}
	affs, err := loadAffiliations(root)
	if err != nil {
		return err
	}
	states := loadStates(affs)
	learnIDs(states)
	all, err := listAllAgents()
	if err != nil {
		return err
	}
	printTree(os.Stdout, thisMachine(), affs, states, all)
	return nil
}

func loadStates(affs []affiliation) map[string]affState {
	states := map[string]affState{}
	for _, af := range affs {
		if st, err := loadState(af); err == nil {
			states[af.ID] = st
		}
	}
	return states
}

// tsumugi 新しく <名前>: 同じ名前のまま、新しい会話で最初の指示から始め直す。
// 動いていれば止め（作業中なら断る）、いまの会話を claude の一覧から外して「前の会話」に残し、最初の指示で起こす。
// ★自分自身（このコマンドを打ったセッション）は止められないので断る。
func cmdRenew(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("使い方: tsumugi 新しく <名前>")
	}
	name := args[0]
	if os.Getenv("TSUMUGI_SESSION") == name {
		return fmt.Errorf("%s は、いまこのコマンドを打っているセッションです。自分は止められないので、シェル（紡の外）から打ってください", name)
	}
	root, err := homeDir()
	if err != nil {
		return err
	}
	unlock, err := lockHome(root, 2*time.Minute)
	if err != nil {
		return err
	}
	defer unlock()
	affs, err := loadAffiliations(root)
	if err != nil {
		return err
	}
	af, spec, ok := findSpec(affs, name)
	if !ok {
		return fmt.Errorf("%q というセッションは、どの所属.json にもありません", name)
	}
	if !spec.here(thisMachine()) {
		return fmt.Errorf("%s は、よそのマシン（%s）のセッションです。そのマシンで打ってください", name, spec.Machine)
	}
	st, err := loadState(af)
	if err != nil {
		return err
	}
	if st.Sessions == nil {
		st.Sessions = map[string]sessionState{}
	}
	ss := st.Sessions[name]
	all, err := listAllAgents()
	if err != nil {
		return err
	}
	resumed, startedNow = map[string]bool{}, map[string]bool{}
	w := os.Stdout
	if a, ok := agentByID(all, ss.SessionID); ok {
		if a.Pid > 0 {
			if a.Status != "idle" {
				return fmt.Errorf("%s は作業中です（%s）。空いているときに打ってください", name, a.Status)
			}
			if _, err := runClaude("stop", a.ID); err != nil {
				return err
			}
			waitStoppedID(a.SessionID, 20*time.Second)
		}
		if _, err := runClaude("rm", a.ID); err != nil && !strings.Contains(err.Error(), "No session matching") {
			fmt.Fprintf(w, "（いまの会話 %s は一覧から外せませんでした: %v）\n", short(a.SessionID), err)
		}
	}
	if ss.SessionID != "" {
		ss.Previous = append(ss.Previous, prevSession{ID: ss.SessionID, Until: time.Now().Format(time.RFC3339), Why: "tsumugi 新しく"})
	}
	ss.SessionID = ""
	st.Sessions[name] = ss
	if err := saveState(af, st); err != nil {
		return err
	}
	spec.From = ""
	if spec.Trust {
		if err := trustBeforeStart(w, action{Affiliation: af.ID, Name: name, Dir: spec.Dir}, false); err != nil {
			return err
		}
	}
	if _, err := runClaudeIn(spec.Dir, startArgs(af, spec)...); err != nil {
		return err
	}
	a, ok := waitRunning(name, 30*time.Second)
	if ok {
		ss.SessionID = a.SessionID
		st.Sessions[name] = ss
		if err := saveState(af, st); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s/%s: 新しい会話 %s で、最初の指示から始め直しました（前の会話は tsumugi tree に出ます）\n", af.ID, name, short(a.SessionID))
		return nil
	}
	fmt.Fprintf(w, "%s/%s: 新しい会話で起こしました（まだ一覧に出ないので、次の run が控えます）\n", af.ID, name)
	return nil
}
