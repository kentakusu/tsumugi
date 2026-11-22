package main

// 名前を変える: 紡のセッションの名前を変える。
//
//   tsumugi 名前を変える <いまの名前> <新しい名前>
//
// Claude Code には、動いている背景セッションの名前を外から変える口が無く、セッションは自分の名前を
// 起動時の環境変数（TSUMUGI_SESSION）でも持つ（結の名乗り・チャネルのソケットに使う）。そこで、
// 新しい名前で、同じ会話を引き継いで起こし直す（会話の中身は続く。会話 ID は新しくなる）。
//
// やること（錠を取って）:
//   1. 所属.json の項の名前を変え、「引き継ぐ会話」にいまの会話 ID、「最初の指示」に名前が変わった知らせを一時的に入れる
//   2. 状態.json の控え（定時を届けた記録）を新しい名前へ移し、届け待ちの宛先も書き換える
//   3. いまのセッションを止め、新しい名前で起こす
//   4. 所属.json の一時の 2 つを元に戻す
// ★自分自身（このコマンドを打ったセッション）は止められないので断る。シェルから打つ。
// ★作業中のセッションは止めない（断る）。空いているときに打つ。
// ★結の名乗りは新しい名前になる（前の名前の書き手とは別になる）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func cmdRename(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("使い方: tsumugi 名前を変える <いまの名前> <新しい名前>")
	}
	oldName, newName := args[0], args[1]
	if oldName == newName || newName == "" || isDigits(newName) {
		return fmt.Errorf("新しい名前が使えません: %q（同じ名前・空・数字だけは不可）", newName)
	}
	if os.Getenv("TSUMUGI_SESSION") == oldName {
		return fmt.Errorf("%s は、いまこのコマンドを打っているセッションです。自分は止められないので、シェル（紡の外）から打ってください", oldName)
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
	af, spec, ok := findSpec(affs, oldName)
	if !ok {
		return fmt.Errorf("%q というセッションは、どの所属.json にもありません", oldName)
	}
	if _, _, taken := findSpec(affs, newName); taken {
		return fmt.Errorf("%q はもう使われています", newName)
	}
	if !spec.here(thisMachine()) {
		return fmt.Errorf("%s は、よそのマシン（%s）のセッションです。そのマシンで打ってください", oldName, spec.Machine)
	}
	st, err := loadState(af)
	if err != nil {
		return err
	}
	prev := st.Sessions[oldName]
	all, err := listAllAgents()
	if err != nil {
		return err
	}
	learnIDs(map[string]affState{af.ID: st})
	running, isRunning := newestRunning(all, oldName)
	if isRunning && running.Status != "idle" {
		return fmt.Errorf("%s は作業中です（%s）。空いているときに打ってください", oldName, running.Status)
	}
	sid := prev.SessionID
	if isRunning {
		sid = running.SessionID
	}

	// 1. 所属.json（順を保って書き換える）
	raw, err := os.ReadFile(af.Path)
	if err != nil {
		return err
	}
	doc, err := parseOrdered(raw)
	if err != nil {
		return err
	}
	item := findSessionItem(doc, oldName)
	if item == nil {
		return fmt.Errorf("%s の中に %s の項が見つかりません", af.Path, oldName)
	}
	origPrompt, hadPrompt := item.vals["最初の指示"]
	origFrom, hadFrom := item.vals["引き継ぐ会話"]
	item.set("名前", newName)
	if sid != "" {
		item.set("引き継ぐ会話", sid)
		item.set("最初の指示", fmt.Sprintf("【紡】このセッションの名前が %s から %s に変わりました（会話はそのまま続いています）。"+
			"結の名乗りも新しい名前になります。続きの作業があれば続け、無ければ待機してください。", oldName, newName))
	}
	if err := writeOrdered(af.Path, doc); err != nil {
		return err
	}
	restore := func() error {
		raw, err := os.ReadFile(af.Path)
		if err != nil {
			return err
		}
		doc, err := parseOrdered(raw)
		if err != nil {
			return err
		}
		it := findSessionItem(doc, newName)
		if it == nil {
			return nil
		}
		putBack(it, "最初の指示", origPrompt, hadPrompt)
		putBack(it, "引き継ぐ会話", origFrom, hadFrom)
		return writeOrdered(af.Path, doc)
	}

	// 2. 控えと届け待ち
	delete(st.Sessions, oldName)
	if err := saveState(af, st); err != nil {
		return err
	}
	if qs, err := loadQueue(af); err == nil {
		for _, q := range qs {
			if q.Name != oldName {
				continue
			}
			q.Name = newName
			b, _ := json.MarshalIndent(q, "", "  ")
			os.WriteFile(q.file, append(b, '\n'), 0o600)
		}
	}

	// 3. 止めて、新しい名前で起こす
	if isRunning {
		fmt.Printf("%s を止めます（%s）\n", oldName, running.ID)
		if _, err := runClaude("stop", running.ID); err != nil {
			restore()
			return fmt.Errorf("止められません: %w", err)
		}
		waitStopped(oldName, 20*time.Second)
		time.Sleep(stopSettle)
	}
	os.Remove(channelSock(root, af.ID, oldName))
	affs, err = loadAffiliations(root)
	if err != nil {
		return err
	}
	naf, nspec, _ := findSpec(affs, newName)
	if err := reconcile(os.Stdout, []affiliation{onlySession(naf, nspec)}, false); err != nil {
		restore()
		return err
	}

	// 4. 一時の 2 つを戻し、定時の記録を引き継ぐ
	if err := restore(); err != nil {
		return err
	}
	st, _ = loadState(naf)
	ns := st.Sessions[newName]
	if prev.Delivered != nil {
		ns.Delivered = prev.Delivered
	}
	st.Sessions[newName] = ns
	if err := saveState(naf, st); err != nil {
		return err
	}
	fmt.Printf("名前を変えました: %s → %s（会話 %s）\n", oldName, newName, short(ns.SessionID))
	return nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func findSessionItem(doc *oobj, name string) *oobj {
	list, _ := doc.vals["セッション"].([]any)
	for _, v := range list {
		if o, ok := v.(*oobj); ok && fmt.Sprint(o.vals["名前"]) == name {
			return o
		}
	}
	return nil
}

func putBack(o *oobj, k string, v any, had bool) {
	if had {
		o.vals[k] = v
		return
	}
	delete(o.vals, k)
	for i, kk := range o.keys {
		if kk == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func writeOrdered(p string, doc *oobj) error {
	b, err := json.MarshalIndent(json.RawMessage(mustMarshal(doc)), "", "  ")
	if err != nil {
		return err
	}
	if fi, err := os.Stat(p); err == nil {
		os.WriteFile(filepath.Join(filepath.Dir(p), "."+filepath.Base(p)+".名前を変える前"), mustRead(p), fi.Mode().Perm())
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func mustMarshal(v any) []byte { b, _ := json.Marshal(v); return b }

func mustRead(p string) []byte { b, _ := os.ReadFile(p); return b }
