package main

// tsumugi 定時（schedules）: 所属.json の定時を、セッションごとに並べる（2026-11-22 鈴木さん）。
//   いつ（cron の式か一度きりの日時）・次に届く時刻・最後に届けた時刻・届かない事情（止めてある・よそのマシン）・指示の頭。
// 読むだけ。所属.json と状態.json を読み、claude は呼ばない。

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// now より後で、いちばん近い発火時刻。無ければ零値（一度きりで過ぎた・cron が読めない・1 年先まで無い）。
func (s scheduleSpec) nextDue(now time.Time) time.Time {
	now = now.Truncate(time.Minute)
	if s.Once != "" {
		t, err := time.ParseInLocation("2006-01-02 15:04", s.Once, time.Local)
		if err == nil && t.After(now) {
			return t
		}
		return time.Time{}
	}
	c, err := parseCron(s.Cron)
	if err != nil {
		return time.Time{}
	}
	end := now.AddDate(1, 0, 1)
	for t := now.Add(time.Minute); t.Before(end); t = t.Add(time.Minute) {
		if !c.f[3][int(t.Month())] {
			// その月に当たらなければ、次の月の頭まで飛ばす
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location()).Add(-time.Minute)
			continue
		}
		if !c.f[2][t.Day()] || !c.f[4][int(t.Weekday())] {
			// その日に当たらなければ、次の日の頭まで飛ばす
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location()).Add(-time.Minute)
			continue
		}
		if c.match(t) {
			return t
		}
	}
	return time.Time{}
}

func cmdSchedules() error {
	root, err := homeDir()
	if err != nil {
		return err
	}
	affs, err := loadAffiliations(root)
	if err != nil {
		return err
	}
	printSchedules(os.Stdout, thisMachine(), affs, loadStates(affs), time.Now())
	return nil
}

func printSchedules(w io.Writer, machine string, affs []affiliation, states map[string]affState, now time.Time) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "セッション\tいつ\t次に届く\t最後に届けた\t\t指示")
	n := 0
	for _, af := range affs {
		for _, s := range af.Sessions {
			ss := states[af.ID].Sessions[s.Name]
			for _, sc := range s.Schedules {
				n++
				when := sc.Cron
				if sc.Once != "" {
					when = sc.Once + "（一度きり）"
				}
				next := "-"
				if t := sc.nextDue(now); !t.IsZero() {
					next = t.Format("01-02 15:04") + "（" + untilLabel(t.Sub(now)) + "）"
				} else if sc.Once != "" {
					next = "（過ぎた）"
				}
				last := "-"
				if v := ss.Delivered[scheduleKey(sc)]; v != "" {
					if t, err := time.Parse(time.RFC3339, v); err == nil {
						last = t.Format("01-02 15:04")
					} else {
						last = v
					}
				}
				note := ""
				switch {
				case !s.here(machine):
					note = "よそのマシン（" + s.Machine + "）"
				case ss.Held:
					note = "★止めてある間は届かない"
				}
				fmt.Fprintf(tw, "%s/%s\t%s\t%s\t%s\t%s\t%s\n", af.ID, s.Name, when, next, last, note, headOf(sc.Prompt, 40))
			}
		}
	}
	tw.Flush()
	if n == 0 {
		fmt.Fprintln(w, "（定時はありません。所属.json のセッションの「定時」に書きます）")
	}
}

// あと何分・何時間か。
func untilLabel(d time.Duration) string {
	m := int(d.Round(time.Minute).Minutes())
	switch {
	case m < 60:
		return fmt.Sprintf("あと %d 分", m)
	case m < 24*60:
		return fmt.Sprintf("あと %d 時間 %d 分", m/60, m%60)
	default:
		return fmt.Sprintf("あと %d 日", m/(24*60))
	}
}

// 指示の頭（改行は空白に）。
func headOf(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
