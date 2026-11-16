package main

// 望む状態に寄せる。足りないものを起こし、紡が起こしたのに要らなくなったものを止める。
//
// 紡が起こしていないセッション（手で開いたものなど）には触らない。
// 何度流しても同じ結果になる（揃っていれば何もしない）。

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// 紡が起こすセッションに必ず付ける設定。ローカルメモリに頼らない（README「決めていること」）。
var fixedSettings = map[string]any{"autoMemoryEnabled": false}

// やること 1 つ。
type action struct {
	Affiliation string
	Name        string
	Verb        string // 起こす / 起こし直す / 写して起こす / 止める
	Dir         string
	Args        []string
}

// 突き合わせて、やることを決める（claude は呼ばない）。
func plan(affs []affiliation, states map[string]affState, all []agent) []action {
	return planOn(thisMachine(), affs, states, all)
}

func planOn(machine string, affs []affiliation, states map[string]affState, all []agent) []action {
	running := map[string]agent{}
	for _, a := range all {
		if a.Pid > 0 {
			running[a.Name] = a
		}
	}
	var acts []action
	for _, af := range affs {
		st := states[af.ID]
		want := map[string]bool{}
		for _, s := range af.Sessions {
			want[s.Name] = true // よそのマシンのものも「要る」なので止めない
			if !s.here(machine) {
				continue
			}
			if _, ok := running[s.Name]; ok {
				continue
			}
			act := action{Affiliation: af.ID, Name: s.Name, Dir: s.Dir}
			if prev, ok := st.Sessions[s.Name]; ok && prev.SessionID != "" {
				// 起こした時の設定は claude 側が覚えている。フラグを付けると写しになるので付けない。
				act.Verb, act.Args = "起こし直す", []string{"--bg", "--resume", prev.SessionID}
			} else {
				act.Verb, act.Args = "起こす", startArgs(af.ID, s)
				if s.From != "" {
					act.Verb = "写して起こす"
				}
			}
			acts = append(acts, act)
		}
		var gone []string
		for name := range st.Sessions {
			if af.only {
				break
			}
			if !want[name] {
				gone = append(gone, name)
			}
		}
		sort.Strings(gone)
		for _, name := range gone {
			act := action{Affiliation: af.ID, Name: name, Verb: "止める"}
			if a, ok := running[name]; ok && a.SessionID == st.Sessions[name].SessionID {
				act.Args = []string{"stop", a.ID}
			}
			acts = append(acts, act) // Args が空なら控えから消すだけ
		}
	}
	return acts
}

// 紡のセッションに必ず渡す決まり。
const yuiRule = "知識（結・yui）は必ず `tsumugi yui '<問い合わせ>'` で、結のデーモンを通して読み書きする。" +
	"yui コマンドで .yui ファイルを直接開かない。デーモンにつながらないときは、人に知らせて待つ。" +
	"Claude Code のローカルメモリには書かない。"

func startArgs(aff string, s sessionSpec) []string {
	set := map[string]any{"env": map[string]string{"TSUMUGI_AFFILIATION": aff, "TSUMUGI_SESSION": s.Name}}
	for k, v := range fixedSettings {
		set[k] = v
	}
	settings, _ := json.Marshal(set)
	args := []string{"--bg", "-n", s.Name, "--settings", string(settings), "--append-system-prompt", yuiRule}
	if s.Permission != "" {
		args = append(args, "--permission-mode", s.Permission)
	}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.From != "" {
		return append(args, "--resume", s.From) // 動いている会話なら写しになる
	}
	if s.Prompt != "" {
		args = append(args, s.Prompt)
	}
	return args
}

// 動いている同名のうち、いちばん新しいもの。
func newestRunning(all []agent, name string) (agent, bool) {
	var best agent
	for _, a := range all {
		if a.Name == name && a.Pid > 0 && a.StartedAt > best.StartedAt {
			best = a
		}
	}
	return best, best.Name != ""
}

func reconcile(w io.Writer, affs []affiliation, dry bool) error {
	if machineUnpinnedOnWSL() {
		fmt.Fprintf(w, "★WSL の上ですが、紡.json に「マシン」がありません。いまの名前は %q です（自動で動くときは WSL_DISTRO_NAME が無く、名前が変わることがあります）\n", thisMachine())
	}
	states := map[string]affState{}
	for _, af := range affs {
		st, err := loadState(af)
		if err != nil {
			return err
		}
		states[af.ID] = st
	}
	// 更新（時刻が来ていれば）。claude が新しくなっても、動いているセッションは下で入れ直す。
	if root, err := homeDir(); err == nil {
		if g, err := loadGlobal(root); err == nil {
			maybeUpdate(w, root, g, time.Now(), dry)
		}
	}
	all, err := listAllAgents()
	if err != nil {
		return err
	}
	// 届け待ちと定時を先に渡す（止まっているものは、指示を付けて起こすので、下の「起こし直す」と重ならない）。
	queuedDone, err := deliverQueue(w, affs, states, all, time.Now(), dry)
	if err != nil {
		return err
	}
	if queuedDone {
		if all, err = listAllAgents(); err != nil {
			return err
		}
	}
	if delivered, err := deliverSchedules(w, affs, states, all, time.Now(), dry); err != nil {
		return err
	} else if delivered && !dry {
		for _, af := range affs {
			if err := saveState(af, states[af.ID]); err != nil {
				return err
			}
		}
		if all, err = listAllAgents(); err != nil {
			return err
		}
	}
	// 古い版で動いている空きセッションを入れ直す（紡.json の「更新」「入れ直し」）。
	if root, err := homeDir(); err == nil {
		if g, err := loadGlobal(root); err == nil && g.Update != nil && g.Update.Restart {
			if restartStale(w, affs, states, all, dry) && !dry {
				if all, err = listAllAgents(); err != nil {
					return err
				}
			}
		}
	}
	acts := plan(affs, states, all)
	if len(acts) == 0 {
		fmt.Fprintln(w, "揃っています。")
		watch(w, affs, states, all, time.Now(), dry)
		if dry {
			return nil
		}
		return record(affs, states, all) // 前回 控え損ねたものを拾う
	}
	var firstErr error
	for _, act := range acts {
		fmt.Fprintf(w, "%s/%s: %s", act.Affiliation, act.Name, act.Verb)
		if dry {
			fmt.Fprintf(w, "（しない）claude %q\n", act.Args)
			continue
		}
		if len(act.Args) > 0 {
			if _, err := runClaudeIn(act.Dir, act.Args...); err != nil {
				fmt.Fprintln(w, " … 失敗")
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		fmt.Fprintln(w, " … した")
		if act.Verb != "止める" {
			waitRunning(act.Name, 15*time.Second) // 起こした直後は claude agents にまだ出ないことがある
		}
		if act.Verb == "止める" {
			delete(states[act.Affiliation].Sessions, act.Name)
		}
	}
	if dry {
		return nil
	}
	// 起こしたものの会話 ID を控える（次に止まったとき同じ会話を続けるため）。
	all, err = listAllAgents()
	if err != nil {
		return err
	}
	watch(w, affs, states, all, time.Now(), dry)
	if err := record(affs, states, all); err != nil {
		return err
	}
	return firstErr
}

// 動いているものの会話 ID を控える。
func record(affs []affiliation, states map[string]affState, all []agent) error {
	for _, af := range affs {
		st := states[af.ID]
		for _, s := range af.Sessions {
			if a, ok := newestRunning(all, s.Name); ok {
				prev := st.Sessions[s.Name]
				if prev.SessionID != a.SessionID {
					prev.SessionID = a.SessionID
				}
				st.Sessions[s.Name] = prev
			}
		}
		if err := saveState(af, st); err != nil {
			return err
		}
	}
	return nil
}

// 届けるもの 1 つ。
type delivery struct {
	Affiliation, Name, Dir, SessionID, Prompt string
	Running                                   *agent            // 動いていれば
	Keys                                      map[string]string // 定時の鍵 → 発火時刻
}

// いま届けるべき定時を選ぶ（claude は呼ばない）。
func dueDeliveries(machine string, affs []affiliation, states map[string]affState, all []agent, now time.Time) []delivery {
	var out []delivery
	for _, af := range affs {
		st := states[af.ID]
		for _, s := range af.Sessions {
			if !s.here(machine) || len(s.Schedules) == 0 {
				continue
			}
			prev, ok := st.Sessions[s.Name]
			if !ok || prev.SessionID == "" {
				continue // まだ一度も起こしていない（最初の起動に任せる）
			}
			d := delivery{Affiliation: af.ID, Name: s.Name, Dir: s.Dir, SessionID: prev.SessionID, Keys: map[string]string{}}
			var prompts []string
			for _, sc := range s.Schedules {
				due := sc.lastDue(now)
				if due.IsZero() {
					continue
				}
				k, at := scheduleKey(sc), due.Format(time.RFC3339)
				if prev.Delivered[k] >= at {
					continue
				}
				d.Keys[k] = at
				prompts = append(prompts, sc.Prompt)
			}
			if len(prompts) == 0 {
				continue
			}
			d.Prompt = "【定時】" + strings.Join(prompts, "\n【定時】")
			if a, ok := newestRunning(all, s.Name); ok {
				d.Running = &a
			}
			out = append(out, d)
		}
	}
	return out
}

func deliverSchedules(w io.Writer, affs []affiliation, states map[string]affState, all []agent, now time.Time, dry bool) (bool, error) {
	ds := dueDeliveries(thisMachine(), affs, states, all, now)
	any := false
	for _, d := range ds {
		fmt.Fprintf(w, "%s/%s: 定時を届ける", d.Affiliation, d.Name)
		if dry {
			if d.Running != nil && d.Running.Status != "idle" {
				fmt.Fprintf(w, " … 作業中なので次の回に（%s）\n", d.Running.Status)
			} else {
				fmt.Fprintf(w, "（しない）%q\n", d.Prompt)
			}
			continue
		}
		if !deliverPrompt(w, d) {
			continue
		}
		st := states[d.Affiliation]
		ss := st.Sessions[d.Name]
		if ss.Delivered == nil {
			ss.Delivered = map[string]string{}
		}
		for k, at := range d.Keys {
			ss.Delivered[k] = at
		}
		st.Sessions[d.Name] = ss
		any = true
	}
	return any, nil
}

func waitStopped(name string, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(time.Second) {
		if all, err := listAllAgents(); err == nil {
			if _, ok := newestRunning(all, name); !ok {
				return true
			}
		}
	}
	return false
}
