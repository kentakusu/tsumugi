package main

// 名前で一覧・接続する。claude の短い ID を人が覚えなくて済むように。

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"text/tabwriter"
	"time"
)

// 紡が見ているセッションを短く並べる。
func printList(w io.Writer, affs []affiliation, all []agent) {
	printListOn(w, thisMachine(), affs, all)
}

func printListOn(w io.Writer, machine string, affs []affiliation, all []agent) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "番号\t名前\t所属\t実際\tID\tマシン\t作業フォルダ")
	n := 0
	for _, af := range affs {
		for _, s := range af.Sessions {
			n++
			state, id, m := "止まっている", "-", machine
			if !s.resident() {
				state = "止まっている（必要なときに起こす）"
			}
			if !s.here(machine) {
				state, m = "（よそのマシン）", s.Machine
			} else if a, ok := newestRunning(all, s.Name); ok {
				state, id = a.Status, a.ID
			} else if a, ok := newest(all, s.Name); ok {
				id = a.ID
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", n, s.Name, af.ID, state, id, m, s.Dir)
		}
	}
	tw.Flush()
}

// 同名のうち、いちばん新しく起きたもの（止まっていても）。
func newest(all []agent, name string) (agent, bool) {
	var best agent
	for _, a := range all {
		if a.Name == name && a.ID != "" && a.StartedAt > best.StartedAt {
			best = a
		}
	}
	return best, best.Name != ""
}

func findSpec(affs []affiliation, name string) (affiliation, sessionSpec, bool) {
	for _, af := range affs {
		for _, s := range af.Sessions {
			if s.Name == name {
				return af, s, true
			}
		}
	}
	return affiliation{}, sessionSpec{}, false
}

// list の番号を名前に直す。番号は所属（フォルダ名順）→ 所属.json の並び順で振る。
// 数字だけの名前は番号として読む（セッション名を数字だけにしない）。
func resolveName(affs []affiliation, arg string) (string, error) {
	n, err := strconv.Atoi(arg)
	if err != nil {
		return arg, nil
	}
	i := 0
	for _, af := range affs {
		for _, s := range af.Sessions {
			if i++; i == n {
				return s.Name, nil
			}
		}
	}
	return "", fmt.Errorf("番号 %d のセッションは在りません（tsumugi list で見られます）", n)
}

// 名前（または list の番号）で開く。紡のセッションが止まっていれば、同じ会話のまま起こしてから開く。
func cmdAttach(arg string) error {
	root, err := homeDir()
	if err != nil {
		return err
	}
	affs, err := loadAffiliations(root)
	if err != nil {
		return err
	}
	learnAll(affs)
	name, err := resolveName(affs, arg)
	if err != nil {
		return err
	}
	all, err := listAllAgents()
	if err != nil {
		return err
	}
	a, ok := newestRunning(all, name)
	if !ok {
		af, s, managed := findSpec(affs, name)
		if !managed {
			if a, ok = newest(all, name); !ok {
				a, ok = byID(all, name)
			}
			if !ok {
				return fmt.Errorf("「%s」というセッションは在りません（tsumugi list で見られます）", name)
			}
		} else {
			fmt.Fprintf(os.Stderr, "%s は止まっているので起こします…\n", name)
			if err := reconcile(os.Stderr, []affiliation{onlySession(af, s)}, false); err != nil {
				return err
			}
			if a, ok = waitRunning(name, 20*time.Second); !ok {
				return fmt.Errorf("%s を起こせませんでした（tsumugi status で確かめてください）", name)
			}
		}
	}
	// ★定時・届けは「止めて、指示を付けて起こす」で渡すので、開いている間に数秒セッションが落ちる
	// （Claude Code に、動いている背景セッションへ外から指示を入れる口が無いため）。そのとき attach は
	// 「has exited」で終わる。同じ会話が起き直してきたら、つなぎ直す（2026-11-21 Reatex の報告）。
	for tries := 0; ; tries++ {
		cmd := exec.Command("claude", "attach", a.ID)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		runErr := cmd.Run()
		if tries >= 5 {
			return runErr
		}
		all, err := listAllAgents()
		if err != nil {
			return runErr
		}
		if cur, ok := byID(all, a.ID); ok && cur.Pid > 0 {
			return runErr // セッションは動いている＝人が自分で抜けた
		}
		fmt.Fprintf(os.Stderr, "\n%s がいま起こし直されています（紡が指示を渡しているところ）。つなぎ直します…\n", name)
		b, ok := waitRunningID(name, a.SessionID, 30*time.Second)
		if !ok {
			return runErr
		}
		a = b
	}
}

// 所属を 1 本のセッションだけに絞る（ほかのセッションを止めないよう、控えもその 1 本ぶんだけ扱う）。
// 人が名前を指して起こす（開く・名前を変える）ときに使うので、常駐しないものも起こす。
func onlySession(af affiliation, s sessionSpec) affiliation {
	s.Resident = nil
	af.Sessions = []sessionSpec{s}
	af.only = true
	return af
}

func waitRunning(name string, d time.Duration) (agent, bool) {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(time.Second) {
		if all, err := listAllAgents(); err == nil {
			if a, ok := newestRunning(all, name); ok {
				return a, true
			}
		}
	}
	return agent{}, false
}

// claude の短い ID（list の ID 列）で探す。
func byID(all []agent, id string) (agent, bool) {
	for _, a := range all {
		if a.ID == id {
			return a, true
		}
	}
	return agent{}, false
}
