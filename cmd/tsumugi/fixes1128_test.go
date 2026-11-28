package main

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
)

// 紡が起こすセッションは、Remote Control にも紡の名前で出す（-n だけでなく --remote-control <名前>）。
func TestStartArgsRemoteControlName(t *testing.T) {
	args := startArgs(affiliation{ID: "k"}, sessionSpec{Name: "tsumugi-windows", Prompt: "p"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-n tsumugi-windows") || !strings.Contains(joined, "--remote-control tsumugi-windows") {
		t.Fatalf("名前が渡っていない: %q", args)
	}
	// --remote-control は値を省ける旗なので、すぐ後ろが名前であること（指示に食われない）
	for i, a := range args {
		if a == "--remote-control" && (i+1 >= len(args) || args[i+1] != "tsumugi-windows") {
			t.Fatalf("--remote-control の後ろが名前でない: %q", args)
		}
	}
	if args[len(args)-1] != "p" {
		t.Fatalf("指示が最後にない: %q", args)
	}
}

// 画面にフォルダの信頼の確認が出たか（空白が抜けた画面でも）。
func TestAskedTrust(t *testing.T) {
	for s, want := range map[string]bool{
		"Quick safety check: ... Yes, I trust this folder\nNo, exit":                 true,
		"❯No,exit\nYes,Itrustthisfolder":                                             true,
		"Created cloud session: x\nView: https://claude.ai/code/session_ABCDEFGHIJK": false,
	} {
		if askedTrust(s) != want {
			t.Fatalf("%q: %v のはず", s, want)
		}
	}
}

// macOS で LAN へつなげない（no route to host）ときだけ、ローカルネットワークの許可の直し方を出す。
func TestMacLocalNetworkHint(t *testing.T) {
	unreach := fmt.Errorf("dial tcp 192.168.11.7:7952: connect: %w", syscall.EHOSTUNREACH)
	if h := macLocalNetworkHint("darwin", unreach); !strings.Contains(h, "ローカルネットワーク") || !strings.Contains(h, macSignID) {
		t.Fatalf("macOS の no route to host で直し方を出していない: %q", h)
	}
	if h := macLocalNetworkHint("linux", unreach); h != "" {
		t.Fatalf("macOS 以外で出した: %q", h)
	}
	if h := macLocalNetworkHint("darwin", errors.New("connection refused")); h != "" {
		t.Fatalf("別の失敗で出した: %q", h)
	}
}
