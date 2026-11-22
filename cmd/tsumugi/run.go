package main

// 望む状態に寄せる。足りないものを起こし、紡が起こしたのに要らなくなったものを止める。
//
// 紡が起こしていないセッション（手で開いたものなど）には触らない。
// 何度流しても同じ結果になる（揃っていれば何もしない）。

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
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
	Trust       bool // 起こす直前に作業フォルダを信頼済みにする（所属.json の「信頼する」）
	Refit       bool // 起こし直すが、claude 側が設定を覚えていないので、紡のフラグを付け直す（同じ ID のまま）
	// 付け直し（Refit）に使う
	af        affiliation
	spec      sessionSpec
	SessionID string
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
	// 名前が変わった写しでも、控えた会話 ID で動いていれば「動いている」
	for _, st := range states {
		for name, ss := range st.Sessions {
			for _, a := range all {
				if a.Pid > 0 && ss.SessionID != "" && a.SessionID == ss.SessionID {
					running[name] = a
				}
			}
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
			if !s.resident() {
				continue // 常駐しない: 起こすのは定時・届けが来たときだけ（deliverSchedules・deliverQueue）
			}
			if st.Sessions[s.Name].Held {
				continue // tsumugi 止める --しばらく: tsumugi a で外すまで起こさない
			}
			act := action{Affiliation: af.ID, Name: s.Name, Dir: s.Dir}
			if prev, ok := st.Sessions[s.Name]; ok && prev.SessionID != "" {
				act.Verb = "起こし直す"
				if jobKeepsSettings(prev.SessionID, s.Name) {
					// 起こした時の設定は claude 側が覚えている。claude respawn で、同じ ID・同じ名前・同じ設定のまま起こす
					// （2026-11-22 Windows・2.1.330 で実測: 止めた会話が同じ ID で、前の会話も持って起きた）。
					act.Args = []string{"respawn", short(prev.SessionID)}
				} else {
					// ★claude 側が設定を覚えていない（job の記録が無い・名前が自動で付け替わった・failed。jobrecord.go）。
					// 同じ会話 ID のまま紡の設定で付け直す（refitSameID: claude rm → フラグ付きの --resume ＋知らせ）。
					// 写しは作らない。Args は --dry で見せる形。
					spec := s
					spec.Prompt, spec.From = refitNotice, prev.SessionID
					act.Args, act.Refit = startArgs(af, spec), true
					act.af, act.spec, act.SessionID = af, s, prev.SessionID
				}
			} else {
				act.Verb, act.Args = "起こす", startArgs(af, s)
				act.Trust = s.Trust
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
	"Claude Code のローカルメモリには書かない。" +

	"人に聞きたいことがあるときは、質問の道具（AskUserQuestion）は使わず、文で聞いてターンを終える（無人で止まらないため）。"

func startArgs(af affiliation, s sessionSpec) []string {
	aff := af.ID
	set := map[string]any{"env": map[string]string{"TSUMUGI_AFFILIATION": aff, "TSUMUGI_SESSION": s.Name}}
	for k, v := range fixedSettings {
		set[k] = v
	}
	settings, _ := json.Marshal(set)
	args := []string{"--bg"}
	if channelOn() {
		// ★--channels は値をいくつでも取るので、すぐ後に別のフラグ（-n）を置く。
		// 指示（最後の引数）の直前に置くと、指示がチャネルの名前として食われる（実測）。
		args = append(args, "--channels", channelPlugin)
	}
	// ★無人のセッションでは、質問の画面（AskUserQuestion）が人の答えを待って止まる（askUserQuestionTimeout は
	// 背景のセッションに効かない・2026-11-21 実測）。道具ごと使えなくし、聞くときは文で聞いてターンを終える。
	args = append(args, "--disallowedTools", "AskUserQuestion")
	args = append(args, "-n", s.Name, "--settings", string(settings), "--append-system-prompt", yuiRule+layerRule(af))
	if s.Permission != "" {
		args = append(args, "--permission-mode", s.Permission)
	}

	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.From != "" {
		// 動いている会話なら写しになる。止まっている会話だと、同じ ID のまま起きることもある
		// （2.1.326・Reatex 実測。daemon.log「bg claimed-spare」）。どちらでも最初の指示は付けて渡す
		// （付けないと、前のターンのまま止まっていて、何も始まらない）。
		args = append(args, "--resume", s.From)
	}
	if s.Prompt != "" {
		args = append(args, s.Prompt)
	}
	return args
}

// 名前 → 紡が控えた会話 ID（状態.json から）。
// ★指示を付けて起こすと、Claude Code が写し（新しい会話 ID）を作り、名前も中身から付け直すことがある
// （2026-11-17 Reatex で実測・2.1.325 で起こし 2.1.326 で渡したとき）。名前だけで追うと見失うので、
// 控えた会話 ID で見つかればそれを先に使う。
var knownIDs = map[string]string{}

func learnIDs(states map[string]affState) {
	knownIDs = map[string]string{}
	for _, st := range states {
		for name, ss := range st.Sessions {
			if ss.SessionID != "" {
				knownIDs[name] = ss.SessionID
			}
		}
	}
}

// 動いているもの: 控えた会話 ID のものがあればそれ、無ければ同名のうちいちばん新しいもの。
func newestRunning(all []agent, name string) (agent, bool) {
	if id := knownIDs[name]; id != "" {
		for _, a := range all {
			if a.SessionID == id && a.Pid > 0 {
				return a, true
			}
		}
	}
	var best agent
	for _, a := range all {
		if a.Name == name && a.Pid > 0 && a.StartedAt > best.StartedAt {
			best = a
		}
	}
	return best, best.Name != ""
}

func reconcile(w io.Writer, affs []affiliation, dry bool) error {
	resumed = map[string]bool{}
	startedNow = map[string]bool{}
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
	learnIDs(states)
	all, err := listAllAgents()
	if err != nil {
		return err
	}
	// ★Claude Code の daemon が立ち上がったばかりなら、この回は何もしない（渡す・起こし直す・控えるのどれも）。
	// daemon は run の最初の claude 呼び出しで立ち上がることがあり（WSL を起こし直したあと）、その直後は
	// 止まった会話を「動いている」と返したり、--resume が空の会話や設定の無い写しを作ったりする
	// （2026-11-22 Reatex で実測: daemon start の 0.6 秒後に、動いている扱いの道から設定の無い写しができた）。
	// 1 本だけ扱う回（tsumugi a・名前を変える）は人が待っているので、何もせずに終わらず、落ち着くまで待つ
	// （何も動いていない機では、tsumugi a の最初の claude 呼び出しで daemon が立ち上がるので、いつもここに当たる）。
	only := onlyMode(affs)
	if left := daemonJustStarted(time.Now()); left > 0 && !dry {
		if !only {
			fmt.Fprintf(w, "Claude Code が立ち上がったばかりなので、この回は何もしません（あと %d 秒は待つ）\n", int(left.Seconds())+1)
			return nil
		}
		fmt.Fprintf(w, "Claude Code が立ち上がったばかりなので、落ち着くまで %d 秒待ちます…\n", int(left.Seconds())+1)
		waitDaemonSettle(left)
		if all, err = listAllAgents(); err != nil {
			return err
		}
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
	machine := thisMachine()
	// 動いているものを配下に揃える（手で起こしたもの・紡の設定が抜けたもの。同じ会話 ID のまま付け直す）。
	// 所属.json に無い名前で動いている背景セッション（手で起こしたもの）を、その機の所属に「常駐しない」で足す（lineage.go）。
	// 足したものは、すぐ下の adoptRunning が空いているときに紡の設定で付け直す。
	// ★1 本だけ扱う回（only）は、所属.json の一部しか見ていないので、紡の外のものを足したり、Completed を片付けたりしない
	// （ほかの所属で動いているセッションを「紡の外」と見て、この所属に足してしまう）。
	adopted := false
	if !only {
		adopted = adoptUnknown(w, machine, affs, states, all, dry)
	}
	if (adoptRunning(w, machine, affs, states, all, dry) || adopted) && !dry {
		for _, af := range affs {
			if err := saveState(af, states[af.ID]); err != nil {
				return err
			}
		}
		learnIDs(states)
		if all, err = listAllAgents(); err != nil {
			return err
		}
	}
	// claude agents の Completed のうち、紡が過去に管理していたもの（前の会話・同じ名前の古い会話）を片付ける（lineage.go）。
	if !only && cleanupCompleted(w, machine, affs, states, all, dry) && !dry {
		for _, af := range affs {
			if err := saveState(af, states[af.ID]); err != nil {
				return err
			}
		}
		if all, err = listAllAgents(); err != nil {
			return err
		}
	}
	acts := skipResumed(plan(affs, states, all))
	if len(acts) == 0 {
		fmt.Fprintln(w, "揃っています。")
		watch(w, affs, states, all, time.Now(), dry)
		if dry {
			return nil
		}
		return record(w, affs, states, all) // 前回 控え損ねたものを拾う
	}
	var firstErr error
	for _, act := range acts {
		// ★Claude Code の自動更新は、背景のサービスを建て直して、止まったセッションを自分で起こし直す。
		// そのすぐ後だと、一覧にまだ出ていないだけのセッションへ --resume して写しを作る
		// （2026-11-21 07:48 実測: 2.1.330 への更新直後に 2 つの写しができた）。起こし直す前に少し待って見直す。
		if act.Verb == "起こし直す" && !dry {
			if left := daemonJustStarted(time.Now()); left > 0 {
				fmt.Fprintf(w, "%s/%s: Claude Code が立ち上がったばかりなので、起こし直しは次の回に（あと %d 秒は待つ）\n", act.Affiliation, act.Name, int(left.Seconds())+1)
				continue
			}
			if _, back := waitRunningID(act.Name, states[act.Affiliation].Sessions[act.Name].SessionID, settleWait); back {
				fmt.Fprintf(w, "%s/%s: 起こし直そうとしたら、自分で起きていた（Claude Code が起こし直した）\n", act.Affiliation, act.Name)
				continue
			}
		}
		if act.Trust {
			if err := trustBeforeStart(w, act, dry); err != nil {
				fmt.Fprintf(w, "%s/%s: 作業フォルダを信頼済みにできないので起こしません: %v\n", act.Affiliation, act.Name, err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		fmt.Fprintf(w, "%s/%s: %s", act.Affiliation, act.Name, act.Verb)
		if act.Refit {
			fmt.Fprint(w, "（Claude Code が設定を覚えていないので、紡の設定を付け直す）")
		}
		if dry {
			fmt.Fprintf(w, "（しない）claude %q\n", act.Args)
			continue
		}
		if act.Refit {
			if !refitSameID(w, act.af, act.spec, act.SessionID, refitNotice, nil) {
				continue
			}
		} else if len(act.Args) > 0 {
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
			startedNow[act.Name] = true
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
	if err := record(w, affs, states, all); err != nil {
		return err
	}
	return firstErr
}

// 起こす直前に、作業フォルダを信頼済みにする。
func trustBeforeStart(w io.Writer, act action, dry bool) error {
	cfg, err := claudeConfigPath()
	if err != nil {
		return err
	}
	if dry {
		fmt.Fprintf(w, "%s/%s: 作業フォルダ %s を信頼済みにする（しない・%s）\n", act.Affiliation, act.Name, act.Dir, cfg)
		return nil
	}
	root, err := homeDir()
	if err != nil {
		return err
	}
	wrote, err := trustFolder(cfg, act.Dir, root)
	if err != nil {
		return err
	}
	if wrote {
		fmt.Fprintf(w, "%s/%s: 作業フォルダ %s を信頼済みにした（%s・控えは %s）\n", act.Affiliation, act.Name, act.Dir, cfg, filepath.Join(root, "設定の控え"))
	}
	return nil
}

var settleWait = 30 * time.Second // テストで縮める

// 控えた会話 ID（無ければ名前）で動いているものが出るのを待つ。
func waitRunningID(name, id string, d time.Duration) (agent, bool) {
	for end := time.Now().Add(d); ; time.Sleep(2 * time.Second) {
		if all, err := listAllAgents(); err == nil {
			for _, a := range all {
				if a.Pid > 0 && (a.SessionID == id || (id == "" && a.Name == name)) {
					return a, true
				}
			}
		}
		if !time.Now().Before(end) {
			return agent{}, false
		}
	}
}

// この run で指示を付けて起こしたもの・入れ直したものは、まだ一覧に出ていなくても起こさない。
func skipResumed(acts []action) []action {
	var out []action
	for _, a := range acts {
		if resumed[a.Name] && a.Verb != "止める" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// この run で紡が起こしたもの（控えてよいもの）。
var startedNow = map[string]bool{}

// 動いているものの会話 ID を控える。
// ★控えるのは、紡が起こしたもの（控えが既にあるか、この run で起こしたもの）だけ。
// 所属.json を置いたときに同じ名前のセッションが手で動いていると、それを控えてしまい、
// 以後は「起こし直す」（フラグ無し）になって、最初の指示・結の決まり・メモリを切る設定・env が
// 一度も渡らなかった（2026-11-17 yui-windows の実測）。
func record(w io.Writer, affs []affiliation, states map[string]affState, all []agent) error {
	for _, af := range affs {
		st := states[af.ID]
		for _, s := range af.Sessions {
			if _, known := st.Sessions[s.Name]; !known && !startedNow[s.Name] {
				continue
			}
			if a, ok := newestRunning(all, s.Name); ok {
				prev := st.Sessions[s.Name]
				if prev.SessionID != a.SessionID {
					// ★控えの会話から乗り換えるのは、乗り換え先に会話の中身があるときだけ（jobrecord.go の hasTranscript）。
					// 中身の無い会話を控えると、次からその空の会話を起こし直し、本物の会話を見失う。
					if prev.SessionID != "" && !hasTranscript(a.SessionID) {
						// ★起きたばかりの写しは、指示を処理し終えるまで会話の記録を書かない（2026-11-22 Reatex で実測:
						// 紡が付け直して起こした 73e96e35 を 3 秒後に「中身が無い」と判定したが、30 秒後には 6.9MB あった）。
						// 起きてから transcriptGrace のあいだは判定を次の回に回す（乗り換えず、知らせもしない）。
						if time.Since(time.UnixMilli(a.StartedAt)) < transcriptGrace {
							continue
						}
						fmt.Fprintf(w, "★%s/%s: 同じ名前で、会話の中身が無いセッション（%s）が動いています。紡は控えの会話（%s）から乗り換えません。"+
							"空のセッションなら claude stop %s してください（次の run が控えの会話を起こし直します）\n",
							af.ID, s.Name, short(a.SessionID), short(prev.SessionID), a.ID)
						continue
					}
					old := prev.SessionID
					prev.moveTo(a.SessionID, "写しを引き取った")
					retireOldID(w, old, all)
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
	// 所属とセッションの設定。常駐しないもので、まだ一度も起こしていない（SessionID が空）ときの最初の起動と、
	// Claude Code が設定を覚えていないときの付け直し（deliverPrompt）に使う。
	af   affiliation
	spec sessionSpec
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
			if (!ok || prev.SessionID == "") && s.resident() {
				continue // まだ一度も起こしていない（最初の起動に任せる）
			}
			if prev.Held {
				continue // tsumugi 止める --しばらく: 定時でも起こさない（その回の定時は渡さない）
			}
			// 常駐しないものは、一度も起こしていなくても渡す（SessionID が空なら、最初の指示と一緒に起こす）
			d := delivery{Affiliation: af.ID, Name: s.Name, Dir: s.Dir, SessionID: prev.SessionID, Keys: map[string]string{}, af: af, spec: s}
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
		var ok bool
		var newID string
		if d.SessionID == "" {
			newID, ok = startFresh(w, d.af, d.spec, d.Prompt)
		} else {
			ok, newID = deliverPrompt(w, d)
		}
		if !ok {
			continue
		}
		st := states[d.Affiliation]
		ss := st.Sessions[d.Name]
		if newID != "" {
			ss.moveTo(newID, "写しになった")
		}
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

// 紡.json の「チャネル」。
func channelOn() bool {
	root, err := homeDir()
	if err != nil {
		return false
	}
	g, err := loadGlobal(root)
	return err == nil && g.Channel
}

// 所属ごとの結の層の決まり（紡が起こすセッションへ渡す）。
func layerRule(af affiliation) string {
	root, _ := homeDir()
	g, _ := loadGlobal(root)
	layers := yuiLayers(g)
	def, allowed := affLayers(&af, layers)
	if allowed == nil && len(layers) <= 1 {
		return ""
	}
	names := allowed
	if names == nil {
		for k := range layers {
			names = append(names, k)
		}
		sort.Strings(names)
	}
	return fmt.Sprintf("結は層に分かれている。この所属が使える層: %s。書くのは既定の層（%s）で、`tsumugi yui '<問い合わせ>'` がそこへつなぐ。"+
		"ほかの層は `tsumugi yui --層 <名前> '<問い合わせ>'` で、要るときだけ読む。"+
		"「%s」の層は使う人の決めたこと・好み・所属をまたぐ一般の決まりで、全所属が読む。仕事の中身（案件・顧客・現在地）をそこに書かない。",
		strings.Join(names, "・"), def, personalLayer)
}
