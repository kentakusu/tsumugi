package main

import (
	"strings"
	"testing"
)

// 人の返事待ちで blocked にしていたセッションが、再起動のあと pid の無い blocked の記録として残る
// （2026-11-23 Windows の再起動・yui-windows）。run は pid で見るので、止まっているとして claude respawn で起こし直す。
// 一覧は「blocked」でなく「止まっている」と出す（動いて人を待っているように読めるため）。
func TestStaleBlockedIsStopped(t *testing.T) {
	stubJobRecords(t, map[string]jobRecord{
		"s-yui": {Name: "yui", NameSource: "user", State: "blocked", RespawnFlags: []string{"-n"}},
	})
	affs := []affiliation{{ID: "x", Sessions: []sessionSpec{{Name: "yui", Prompt: "最初の指示", Permission: "bypassPermissions"}}}}
	states := map[string]affState{"x": {Sessions: map[string]sessionState{"yui": {SessionID: "s-yui"}}}}
	stale := agent{ID: "s-yui", SessionID: "s-yui", Name: "yui", Kind: "background", State: "blocked"}
	acts := plan(affs, states, []agent{stale})
	if len(acts) != 1 || acts[0].Verb != "起こし直す" || strings.Join(acts[0].Args, " ") != "respawn s-yui" {
		t.Fatalf("pid の無い blocked は claude respawn で起こし直すはず: %+v", acts)
	}
	if got := agentLabel(stale); got != "止まっている（記録は blocked）" {
		t.Fatalf("pid の無い blocked の表示: %q", got)
	}
	// 対照: 動いている blocked（pid あり）は起こし直さず、status をそのまま出す
	live := stale
	live.Pid, live.Status = 42, "idle"
	if acts := plan(affs, states, []agent{live}); len(acts) != 0 {
		t.Fatalf("動いているものを起こし直した: %+v", acts)
	}
	if got := agentLabel(live); got != "idle" {
		t.Fatalf("動いている blocked の表示: %q", got)
	}
	// 対照: 止めたもの（done）はそのまま done と出す
	if got := agentLabel(agent{State: "done"}); got != "done" {
		t.Fatalf("done の表示: %q", got)
	}
}
