package main

// 定時: 決まった時刻にセッションへ指示を届ける（OS の cron ＋ tmux でやっていたことの置き換え）。
//
// 届け方: 外から指示を渡せるのは「止まっているセッション」だけ（動いているものに
// --resume で指示を付けると写しになる）。なので、
//   止まっている → claude --bg --resume <会話ID> "<指示>"（同じ ID で起きて受け取る）
//   空いている（idle）→ いったん止めてから上と同じ
//   作業中 → 今回は見送り、次の run で届ける（猶予を過ぎたら諦めて記録する）

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 所属.json の「定時」1 つ。「いつ」（cron 5 項目・ローカル時刻）か「日時」（一度だけ）のどちらか。
type scheduleSpec struct {
	Cron   string `json:"いつ,omitempty"`
	Once   string `json:"日時,omitempty"` // "2006-01-02 15:04"
	Prompt string `json:"指示"`
}

// 届けるのが遅れてよい長さ。過ぎたら諦める（夜中に止まっていて朝の分を昼に届ける、をしない）。
const scheduleGrace = 2 * time.Hour

func (s scheduleSpec) validate() error {
	if s.Prompt == "" {
		return fmt.Errorf("「指示」がありません")
	}
	switch {
	case s.Cron != "" && s.Once != "":
		return fmt.Errorf("「いつ」と「日時」は片方だけ書きます")
	case s.Cron != "":
		_, err := parseCron(s.Cron)
		return err
	case s.Once != "":
		_, err := time.ParseInLocation("2006-01-02 15:04", s.Once, time.Local)
		if err != nil {
			return fmt.Errorf("「日時」は 2026-11-28 09:00 の形で書きます: %w", err)
		}
		return nil
	}
	return fmt.Errorf("「いつ」か「日時」が要ります")
}

// now 以前で、いちばん最近の発火時刻（猶予の中だけを見る）。無ければ零値。
func (s scheduleSpec) lastDue(now time.Time) time.Time {
	now = now.Truncate(time.Minute)
	if s.Once != "" {
		t, err := time.ParseInLocation("2006-01-02 15:04", s.Once, time.Local)
		if err == nil && !t.After(now) && now.Sub(t) <= scheduleGrace {
			return t
		}
		return time.Time{}
	}
	c, err := parseCron(s.Cron)
	if err != nil {
		return time.Time{}
	}
	for t := now; now.Sub(t) <= scheduleGrace; t = t.Add(-time.Minute) {
		if c.match(t) {
			return t
		}
	}
	return time.Time{}
}

// cron の 5 項目（分 時 日 月 曜日）。数・*・a-b・a,b・/n。
type cronExpr struct{ f [5]map[int]bool }

func parseCron(s string) (cronExpr, error) {
	var c cronExpr
	parts := strings.Fields(s)
	if len(parts) != 5 {
		return c, fmt.Errorf("「いつ」は cron の 5 項目（分 時 日 月 曜日）で書きます: %q", s)
	}
	lim := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, p := range parts {
		m := map[int]bool{}
		for _, item := range strings.Split(p, ",") {
			step := 1
			if j := strings.Index(item, "/"); j >= 0 {
				n, err := strconv.Atoi(item[j+1:])
				if err != nil || n <= 0 {
					return c, fmt.Errorf("「いつ」の %q が読めません", item)
				}
				step, item = n, item[:j]
			}
			lo, hi := lim[i][0], lim[i][1]
			if item != "*" {
				a, b, isRange := strings.Cut(item, "-")
				x, err := strconv.Atoi(a)
				if err != nil {
					return c, fmt.Errorf("「いつ」の %q が読めません", item)
				}
				lo, hi = x, x
				if isRange {
					if hi, err = strconv.Atoi(b); err != nil {
						return c, fmt.Errorf("「いつ」の %q が読めません", item)
					}
				} else if step > 1 {
					hi = lim[i][1]
				}
			}
			if lo < lim[i][0] || hi > lim[i][1] || lo > hi {
				return c, fmt.Errorf("「いつ」の %q が範囲の外です", item)
			}
			for v := lo; v <= hi; v += step {
				m[v] = true
			}
		}
		c.f[i] = m
	}
	if c.f[4][7] {
		c.f[4][0] = true // 日曜は 0 でも 7 でも
	}
	return c, nil
}

func (c cronExpr) match(t time.Time) bool {
	return c.f[0][t.Minute()] && c.f[1][t.Hour()] && c.f[2][t.Day()] &&
		c.f[3][int(t.Month())] && c.f[4][int(t.Weekday())]
}

// 定時の控えの鍵（状態.json に、最後に届けた発火時刻を持つ）。
func scheduleKey(s scheduleSpec) string {
	if s.Once != "" {
		return "日時 " + s.Once
	}
	return "いつ " + s.Cron + " " + s.Prompt
}
