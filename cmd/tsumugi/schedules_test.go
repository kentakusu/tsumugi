package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestNextDue(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		spec scheduleSpec
		now  string
		want string // 空なら「無い」
	}{
		{scheduleSpec{Cron: "0 9 * * *"}, "2026-11-23 08:30", "2026-11-23 09:00"},
		{scheduleSpec{Cron: "0 9 * * *"}, "2026-11-23 09:00", "2026-11-24 09:00"}, // ちょうどの時刻は「次」ではない
		{scheduleSpec{Cron: "17,47 7-22 * * *"}, "2026-11-23 22:50", "2026-11-24 07:17"},
		{scheduleSpec{Cron: "2 9,13,18 * * *"}, "2026-11-23 13:02", "2026-11-23 18:02"},
		{scheduleSpec{Cron: "0 9 1 * *"}, "2026-11-23 10:00", "2026-12-01 09:00"}, // 月の頭
		{scheduleSpec{Cron: "0 9 * * 1"}, "2026-11-23 10:00", "2026-11-30 09:00"}, // 月曜（11-23 は月曜）
		{scheduleSpec{Cron: "0 0 29 2 *"}, "2026-11-23 10:00", ""},                // 1 年先まで無い（2028 年まで来ない）
		{scheduleSpec{Once: "2026-11-28 09:00"}, "2026-11-23 10:00", "2026-11-28 09:00"},
		{scheduleSpec{Once: "2026-11-20 09:00"}, "2026-11-23 10:00", ""}, // 一度きりで過ぎた
		{scheduleSpec{Cron: "読めない"}, "2026-11-23 10:00", ""},
	} {
		got := c.spec.nextDue(at(c.now))
		if c.want == "" {
			if !got.IsZero() {
				t.Fatalf("%+v @%s: 無いはずが %v", c.spec, c.now, got)
			}
			continue
		}
		if !got.Equal(at(c.want)) {
			t.Fatalf("%+v @%s: %v のはずが %v", c.spec, c.now, c.want, got)
		}
		// 対照: nextDue の時刻で lastDue を引くと、その時刻が返る（cron の当て方が同じ）
		if c.spec.Cron != "" && !c.spec.lastDue(got).Equal(got) {
			t.Fatalf("%+v: nextDue %v が lastDue と食い違う", c.spec, got)
		}
	}
}

// tsumugi 定時: セッションごとに、いつ・次に届く・最後に届けた・止めてあれば届かない・指示の頭を並べる。
func TestPrintSchedules(t *testing.T) {
	now, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-23 08:30", time.Local)
	morning := scheduleSpec{Cron: "0 9 * * *", Prompt: "朝の巡回\n上から順にやる"}
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{
		{Name: "main", Schedules: []scheduleSpec{morning, {Once: "2026-11-20 09:00", Prompt: "済んだ一度きり"}}},
		{Name: "held", Schedules: []scheduleSpec{{Cron: "30 12 * * *", Prompt: "昼"}}},
		{Name: "far", Machine: "other-pc", Schedules: []scheduleSpec{{Cron: "0 18 * * *", Prompt: "夕方"}}},
		{Name: "none"},
	}}}
	states := map[string]affState{"x": {Sessions: map[string]sessionState{
		"main": {SessionID: "M", Delivered: map[string]string{scheduleKey(morning): "2026-11-22T09:00:00+09:00"}},
		"held": {SessionID: "H", Held: true},
	}}}
	var b bytes.Buffer
	printSchedules(&b, "m", affs, states, now)
	out := b.String()
	for _, want := range []string{
		"x/main", "0 9 * * *", "11-23 09:00（あと 30 分）", "11-22 09:00", "朝の巡回 上から順にやる",
		"2026-11-20 09:00（一度きり）", "（過ぎた）",
		"x/held", "★止めてある間は届かない",
		"x/far", "よそのマシン（other-pc）",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q が出ていない:\n%s", want, out)
		}
	}
	if strings.Contains(out, "x/none") {
		t.Fatalf("定時の無いセッションを出した:\n%s", out)
	}
	b.Reset()
	printSchedules(&b, "m", []affiliation{{ID: "y", Sessions: []sessionSpec{{Name: "a"}}}}, nil, now)
	if !strings.Contains(b.String(), "定時はありません") {
		t.Fatalf("定時が無いと知らせていない: %s", b.String())
	}
}
