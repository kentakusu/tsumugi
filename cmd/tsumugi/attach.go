package main

// 名前で一覧・接続する。claude の短い ID を人が覚えなくて済むように。

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"text/tabwriter"
	"time"
)

// 紡が見ているセッションを短く並べる。
func printList(w io.Writer, affs []affiliation, all []agent) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "名前\t所属\t実際\tID\t作業フォルダ")
	for _, af := range affs {
		for _, s := range af.Sessions {
			state, id := "止まっている", "-"
			if a, ok := newestRunning(all, s.Name); ok {
				state, id = a.Status, a.ID
			} else if a, ok := newest(all, s.Name); ok {
				id = a.ID
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name, af.ID, state, id, s.Dir)
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

// 名前で開く。紡のセッションが止まっていれば、同じ会話のまま起こしてから開く。
func cmdAttach(name string) error {
	root, err := homeDir()
	if err != nil {
		return err
	}
	affs, err := loadAffiliations(root)
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
	cmd := exec.Command("claude", "attach", a.ID)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// 所属を 1 本のセッションだけに絞る（ほかのセッションを止めないよう、控えもその 1 本ぶんだけ扱う）。
func onlySession(af affiliation, s sessionSpec) affiliation {
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
