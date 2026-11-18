package main

// 見張り: 紡のセッションが止まったまま（権限の確認・入力待ちなど）や、作業が長すぎるのを
// run のたびに見て、続いていたら知らせる。
//
// 様子は claude agents --json の state が blocked なら blocked、ほかは status（busy / idle）。blocked は人の手を待っている。
// 1 つの「続き」につき知らせるのは 1 回。様子が変われば数え直す。

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// 所属.json の「見張り」。
type watchSpec struct {
	Blocked string   `json:"止まり,omitempty"`  // 例 "15m"。blocked がこれだけ続いたら知らせる
	Long    string   `json:"長すぎ,omitempty"`  // 例 "3h"。作業中がこれだけ続いたら知らせる
	Notify  []string `json:"知らせる,omitempty"` // 知らせるコマンド。本文は標準入力と TSUMUGI_MESSAGE で渡す
}

const (
	defaultBlocked = 15 * time.Minute
	defaultLong    = 3 * time.Hour
)

func (w watchSpec) durations() (blocked, long time.Duration, err error) {
	blocked, long = defaultBlocked, defaultLong
	if w.Blocked != "" {
		if blocked, err = time.ParseDuration(w.Blocked); err != nil {
			return 0, 0, fmt.Errorf("見張りの「止まり」は 15m のように書きます: %w", err)
		}
	}
	if w.Long != "" {
		if long, err = time.ParseDuration(w.Long); err != nil {
			return 0, 0, fmt.Errorf("見張りの「長すぎ」は 3h のように書きます: %w", err)
		}
	}
	return blocked, long, nil
}

// 状態.json に持つ、セッションの様子の続き。
type watchState struct {
	State    string `json:"様子"`
	Since    string `json:"から"`
	Notified bool   `json:"知らせた,omitempty"`
}

// 見張る様子。blocked は state で、作業中は status（busy）で見る。
// ★state の working は、作業していなくても出る（2026-11-17 実測: status が idle のまま
// state が working のセッションがある）。state だけで「長すぎ」を数えると毎回鳴る。
func agentState(a agent) string {
	// ★state が blocked でも status が idle なら、ターンは終わっていて「人に何か頼んで待っている」だけ
	// （定時も届けも渡る）。止まりとして数えない（2026-11-18 cc-34 で誤報）。
	// 止まっているのは、status が waiting（質問・権限の確認で止まっている）か blocked のとき。
	if a.Status == "waiting" || a.Status == "blocked" || (a.State == "blocked" && a.Status != "idle") {
		return "blocked"
	}
	return a.Status
}

// 見る。知らせることがあれば知らせて、states を書き換える（保存は呼ぶ側）。
func watch(w io.Writer, affs []affiliation, states map[string]affState, all []agent, now time.Time, dry bool) {
	machine := thisMachine()
	for _, af := range affs {
		blocked, long, err := af.Watch.durations()
		if err != nil {
			fmt.Fprintf(w, "%s: %v\n", af.ID, err)
			continue
		}
		st := states[af.ID]
		for _, s := range af.Sessions {
			if !s.here(machine) {
				continue
			}
			ss, ok := st.Sessions[s.Name]
			if !ok {
				continue
			}
			a, running := newestRunning(all, s.Name)
			if !running || a.SessionID != ss.SessionID {
				ss.Watch = nil
				st.Sessions[s.Name] = ss
				continue
			}
			cur := agentState(a)
			if ss.Watch == nil || ss.Watch.State != cur {
				ss.Watch = &watchState{State: cur, Since: now.Format(time.RFC3339)}
				st.Sessions[s.Name] = ss
				continue
			}
			since, err := time.Parse(time.RFC3339, ss.Watch.Since)
			if err != nil || ss.Watch.Notified {
				continue
			}
			var limit time.Duration
			switch cur {
			case "blocked":
				limit = blocked
			case "busy":
				limit = long
			default:
				continue
			}
			if now.Sub(since) < limit {
				continue
			}
			msg := fmt.Sprintf("【紡・見張り】%s/%s が %s のまま %s たちました（%s から・%s）。`tsumugi a %s` で見てください。",
				af.ID, s.Name, cur, now.Sub(since).Round(time.Minute), since.Format("01/02 15:04"), machine, s.Name)
			fmt.Fprintln(w, msg)
			if dry {
				continue
			}
			notify(w, af, msg)
			ss.Watch.Notified = true
			st.Sessions[s.Name] = ss
		}
	}
}

// 所属の知らせ先へ。無ければ run の出力（journal）に残るだけ。
func notify(w io.Writer, af affiliation, msg string) {
	if len(af.Watch.Notify) == 0 {
		return
	}
	cmd := exec.Command(expandHome(af.Watch.Notify[0]), af.Watch.Notify[1:]...)
	cmd.Stdin = bytes.NewBufferString(msg + "\n")
	cmd.Env = append(os.Environ(), "TSUMUGI_MESSAGE="+msg, "TSUMUGI_IN_RUN=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(w, "%s: 知らせられませんでした: %v: %s\n", af.ID, err, out)
	}
}
