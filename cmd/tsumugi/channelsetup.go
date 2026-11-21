package main

// チャネルの準備（tsumugi チャネル準備）: プラグインを入れ、マシンの管理設定を確かめる。
// 管理設定は管理者の権限で書くので、紡は書かずに、打つものを出す。

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const channelPolicy = `{"channelsEnabled": true, "allowedChannelPlugins": [{"marketplace": "tsumugi", "plugin": "tsumugi-channel"}]}`

// 管理設定の置き場所（drop-in。ほかの方針と混ぜないよう、紡だけのファイルにする）。
func managedDropIn() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode/managed-settings.d/tsumugi.json"
	case "windows":
		return `C:\Program Files\ClaudeCode\managed-settings.d\tsumugi.json`
	}
	return "/etc/claude-code/managed-settings.d/tsumugi.json"
}

func policyOK(p string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	var v struct {
		On      bool `json:"channelsEnabled"`
		Allowed []struct {
			Marketplace, Plugin string
		} `json:"allowedChannelPlugins"`
	}
	if json.Unmarshal(b, &v) != nil || !v.On {
		return false
	}
	for _, a := range v.Allowed {
		if a.Marketplace == "tsumugi" && a.Plugin == "tsumugi-channel" {
			return true
		}
	}
	return false
}

func cmdChannelSetup() error {
	ok := true
	if _, err := exec.LookPath("tsumugi"); err != nil {
		fmt.Println("★tsumugi が PATH にありません。プラグインは `tsumugi channel` を呼ぶので、PATH の通った場所に置いてください")
		ok = false
	} else {
		fmt.Println("○ tsumugi は PATH にある")
	}
	out, _ := runClaude("plugin", "list")
	if strings.Contains(string(out), "tsumugi-channel@tsumugi") {
		fmt.Println("○ プラグイン tsumugi-channel@tsumugi は入っている")
	} else {
		fmt.Println("… プラグインを入れます")
		if b, err := runClaude("plugin", "marketplace", "add", "kentakusu/tsumugi"); err != nil && !strings.Contains(err.Error(), "already") {
			fmt.Printf("★マーケットプレイスを足せません: %v %s\n", err, b)
			ok = false
		}
		if b, err := runClaude("plugin", "install", "tsumugi-channel@tsumugi"); err != nil {
			fmt.Printf("★プラグインを入れられません: %v %s\n", err, b)
			ok = false
		} else {
			fmt.Println("○ プラグインを入れた")
		}
	}
	p := managedDropIn()
	if policyOK(p) {
		fmt.Println("○ 管理設定に許可がある:", p)
	} else {
		ok = false
		fmt.Println("★管理設定に許可がありません。管理者の権限で、次を 1 回打ってください:")
		switch runtime.GOOS {
		case "windows":
			fmt.Printf("  （PowerShell を「管理者として実行」で開いて）\n  New-Item -ItemType Directory -Force '%s' | Out-Null\n  Set-Content -Encoding utf8 -Path '%s' -Value '%s'\n", filepath.Dir(p), p, channelPolicy)
		default:
			fmt.Printf("  sudo mkdir -p '%s' && echo '%s' | sudo tee '%s' >/dev/null\n", filepath.Dir(p), channelPolicy, p)
		}
	}
	root, _ := homeDir()
	if g, err := loadGlobal(root); err == nil && g.Channel {
		fmt.Println("○ 紡.json の「チャネル」が true")
	} else {
		fmt.Printf("… 全部 ○ になったら、%s に \"チャネル\": true を足してください（紡が起こすセッションに --channels を付けるようになる）\n", filepath.Join(root, globalFile))
	}
	if !ok {
		return fmt.Errorf("まだ準備が済んでいません（★の行）")
	}
	return nil
}
