package main

// tsumugi 止める <名前|番号> [--しばらく] [--強制]
//
//   控えの会話 ID で claude stop する（同じ名前の別の会話を止めない）。控えは残すので、次に起こしても同じ会話が続く。
//   --しばらく（--hold）: 「止めておく」印を控えに付ける。印があるあいだは run・定時・届けで起こさない（届けは溜めておく）。
//                        tsumugi a で起こすと印は外れる。一覧（tsumugi l・tree）には「止めてある」と出る。
//   --強制（--force）: 作業中でも止める。
// ★自分自身（このコマンドを打ったセッション）は止められないので断る。

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func cmdStop(args []string) error {
	hold, force := false, false
	var target string
	for _, a := range args {
		switch a {
		case "--しばらく", "--hold":
			hold = true
		case "--強制", "--force":
			force = true
		default:
			if target != "" {
				return fmt.Errorf("使い方: tsumugi 止める <名前|番号> [--しばらく] [--強制]")
			}
			target = a
		}
	}
	if target == "" {
		return fmt.Errorf("使い方: tsumugi 止める <名前|番号> [--しばらく] [--強制]")
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
	name, err := resolveName(affs, target)
	if err != nil {
		return err
	}
	if os.Getenv("TSUMUGI_SESSION") == name {
		return fmt.Errorf("%s は、いまこのコマンドを打っているセッションです。自分は止められないので、シェル（紡の外）から打ってください", name)
	}
	all, err := listAllAgents()
	if err != nil {
		return err
	}
	return stopSession(os.Stdout, affs, name, hold, force, all)
}

func stopSession(w io.Writer, affs []affiliation, name string, hold, force bool, all []agent) error {
	af, spec, ok := findSpec(affs, name)
	if !ok {
		return fmt.Errorf("%q というセッションは、どの所属.json にもありません（tsumugi list で見られます）", name)
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
	a, running := agentByID(all, ss.SessionID)
	running = running && a.Pid > 0
	if running {
		if a.Status != "idle" && !force {
			return fmt.Errorf("%s は作業中です（%s）。空いているときに打つか、--強制 を付けてください", name, a.Status)
		}
		if _, err := runClaude("stop", a.ID); err != nil {
			return err
		}
		if !waitStoppedID(a.SessionID, 20*time.Second) {
			return fmt.Errorf("%s が止まりきりません（tsumugi l で確かめてください）", name)
		}
		fmt.Fprintf(w, "%s/%s: 止めました（会話 %s は控えたまま。次に起こしても同じ会話が続きます）\n", af.ID, name, short(a.SessionID))
	} else {
		fmt.Fprintf(w, "%s/%s: もう止まっています\n", af.ID, name)
	}
	if hold {
		ss.Held, ss.HeldAt = true, time.Now().Format(time.RFC3339)
		st.Sessions[name] = ss
		if err := saveState(af, st); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s/%s: 止めておく印を付けました。run・定時・届けでは起こしません（届けは溜めておきます）。tsumugi a %s で起こすと印は外れます\n", af.ID, name, name)
		return nil
	}
	if spec.resident() && running {
		fmt.Fprintf(w, "★%s は常駐なので、5 分以内に紡の run が起こし直します。止めたままにするなら tsumugi 止める %s --しばらく\n", name, name)
	}
	return nil
}

// tsumugi 外す <名前|番号> [--強制]: 紡の一覧から消す。
// 動いていれば止め（作業中は断る。--強制 なら止める）、いまの会話と、claude の一覧に残っている前の会話を claude rm で外し、
// 所属.json の項と控えを消す。会話の記録（jsonl）は残る（所属.json に「引き継ぐ会話」で書けば戻せる）。
func cmdRemove(args []string) error {
	force := false
	var target string
	for _, a := range args {
		switch a {
		case "--強制", "--force":
			force = true
		default:
			if target != "" {
				return fmt.Errorf("使い方: tsumugi 外す <名前|番号> [--強制]")
			}
			target = a
		}
	}
	if target == "" {
		return fmt.Errorf("使い方: tsumugi 外す <名前|番号> [--強制]")
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
	name, err := resolveName(affs, target)
	if err != nil {
		return err
	}
	if os.Getenv("TSUMUGI_SESSION") == name {
		return fmt.Errorf("%s は、いまこのコマンドを打っているセッションです。自分は外せないので、シェル（紡の外）から打ってください", name)
	}
	all, err := listAllAgents()
	if err != nil {
		return err
	}
	return removeSession(os.Stdout, affs, name, force, all)
}

func removeSession(w io.Writer, affs []affiliation, name string, force bool, all []agent) error {
	af, spec, ok := findSpec(affs, name)
	if !ok {
		return fmt.Errorf("%q というセッションは、どの所属.json にもありません（tsumugi list で見られます）", name)
	}
	if !spec.here(thisMachine()) {
		return fmt.Errorf("%s は、よそのマシン（%s）のセッションです。そのマシンで打ってください", name, spec.Machine)
	}
	st, err := loadState(af)
	if err != nil {
		return err
	}
	ss := st.Sessions[name]
	if a, ok := agentByID(all, ss.SessionID); ok && a.Pid > 0 {
		if a.Status != "idle" && !force {
			return fmt.Errorf("%s は作業中です（%s）。空いているときに打つか、--強制 を付けてください", name, a.Status)
		}
		if _, err := runClaude("stop", a.ID); err != nil {
			return err
		}
		if !waitStoppedID(a.SessionID, 20*time.Second) {
			return fmt.Errorf("%s が止まりきりません（tsumugi l で確かめてください）", name)
		}
	}
	// いまの会話と、claude の一覧に残っている前の会話を外す
	ids := []string{ss.SessionID}
	for _, p := range ss.Previous {
		ids = append(ids, p.ID)
	}
	for _, id := range ids {
		if a, ok := agentByID(all, id); ok {
			if _, err := runClaude("rm", a.ID); err != nil && !strings.Contains(err.Error(), "No session matching") {
				fmt.Fprintf(w, "（会話 %s は claude の一覧から外せませんでした: %v）\n", short(id), err)
			}
		}
	}
	// 所属.json の項を消す（並びとほかの鍵はそのまま）
	raw, err := os.ReadFile(af.Path)
	if err != nil {
		return err
	}
	doc, err := parseOrdered(raw)
	if err != nil {
		return err
	}
	list, _ := doc.vals["セッション"].([]any)
	var kept []any
	for _, v := range list {
		if o, ok := v.(*oobj); ok && fmt.Sprint(o.vals["名前"]) == name {
			continue
		}
		kept = append(kept, v)
	}
	if kept == nil {
		kept = []any{}
	}
	doc.set("セッション", kept)
	if err := writeOrdered(af.Path, doc); err != nil {
		return err
	}
	delete(st.Sessions, name)
	if err := saveState(af, st); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s/%s: 紡の一覧から外しました（所属.json と控えから消し、claude の一覧からも外した）。会話の記録は残ります", af.ID, name)
	if ss.SessionID != "" {
		fmt.Fprintf(w, "（戻すなら所属.json に「引き継ぐ会話」: %q で足す）", ss.SessionID)
	}
	fmt.Fprintln(w)
	return nil
}

// tsumugi a で起こすとき、止めておく印を外す。外したら true。
func releaseHold(af affiliation, name string) (bool, error) {
	st, err := loadState(af)
	if err != nil {
		return false, err
	}
	ss, ok := st.Sessions[name]
	if !ok || !ss.Held {
		return false, nil
	}
	ss.Held, ss.HeldAt = false, ""
	st.Sessions[name] = ss
	return true, saveState(af, st)
}
