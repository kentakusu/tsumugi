// tsumugi — Claude Code を所属ごとに無人で常駐させる。
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"time"
)

// 版: ビルドに埋め込まれた git の commit（go build がリポジトリの中で埋める）。
func version() string {
	rev, at, dirty := "?", "", ""
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if len(s.Value) > 7 {
					rev = s.Value[:7]
				} else {
					rev = s.Value
				}
			case "vcs.time":
				at = " " + s.Value
			case "vcs.modified":
				if s.Value == "true" {
					dirty = "（手元の変更あり）"
				}
			}
		}
	}
	return "tsumugi " + rev + dirty + at
}

const usage = `使い方: tsumugi <コマンド>

  status              所属ごとに、望む状態（所属.json）と実際（claude agents）を並べる
  run [--dry]         望む状態に寄せる（--dry はやることを出すだけ）
  list, l             紡のセッションを番号・名前で並べる（claude agents に出ているものは紡の外のものも全部出す）
  tree, 木            セッションごとに、いまの会話と前の会話を木の形で並べる（紡の外は作業フォルダごと）
  attach, a <名前|番号|ID>
                      開く（止まっていれば同じ会話のまま起こしてから）
  届ける, send <名前> [指示…]
                      セッションへ指示を渡す（無ければ標準入力を 1 件として。すぐ渡せなければ run が渡す）
  更新, update [--dry] [紡|claude|OS|設定項目]…
                      Claude Code と OS などを新しくする（紡.json の「更新」のとおり。無ければ 紡・claude・OS・設定項目）。
                      項目を書けば、それだけ（例: tsumugi 更新 紡）
  設定項目, settings [--dry]
                      Claude Code の設定項目の一覧を取り、前回からの増減と、手元の設定の「一覧に無い鍵」を出す
  yui <問い合わせ>…    結のデーモンに名乗って問い合わせる（無ければ標準入力を 1 件として）
  install [--dry]     OS の定期実行に run を登録する（--dry は書くものを見るだけ）
  uninstall           その登録を外す

  名前を変える, rename <いまの名前> <新しい名前>
                      紡のセッションの名前を変える（同じ会話を引き継いで、新しい名前で起こし直す）
  新しく, renew <名前>  同じ名前のまま、新しい会話で最初の指示から始め直す（前の会話は tree に残る）
  設定そろえ, align-settings [--dry]
                      Claude Code のユーザー設定を基本の値（settings/claude-base.json）と比べ、紡.json で許した鍵だけ合わせる
  チャネル準備, channel-setup
                      動いているセッションへ止めずに渡すチャネルの準備（プラグインと管理設定を確かめる）
  version, --version   版（git の commit）を出す

所属の設定は $TSUMUGI_HOME/<所属>/所属.json（TSUMUGI_HOME が無ければ ~/.tsumugi）。
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	// 知らない引数は黙って捨てない（所属.json の知らないキーと同じ考え。2026-11-17 Reatex の指摘）
	extra := map[string]int{"status": 0, "list": 0, "l": 0, "tree": 0, "木": 0, "run": 1, "install": 1, "uninstall": 1, "version": 0, "--version": 0, "-v": 0, "版": 0}
	if n, ok := extra[os.Args[1]]; ok {
		rest := os.Args[2:]
		if len(rest) > n || (len(rest) == 1 && rest[0] != "--dry") {
			fmt.Fprintf(os.Stderr, "知らない引数です: %v\n\n%s", rest, usage)
			os.Exit(2)
		}
	}
	switch os.Args[1] {
	case "status":
		err = cmdStatus()
	case "list", "l":
		err = cmdList()
	case "attach", "a":
		if len(os.Args) < 3 {
			fmt.Fprint(os.Stderr, "使い方: tsumugi a <名前|番号>\n")
			os.Exit(2)
		}
		err = cmdAttach(os.Args[2])
	case "install", "uninstall":
		dry := len(os.Args) > 2 && os.Args[2] == "--dry"
		err = cmdInstall(dry, os.Args[1] == "uninstall")
	case "yui":
		err = cmdYui(os.Args[2:])
	case "channel":
		err = cmdChannel()
	case "名前を変える", "rename":
		err = cmdRename(os.Args[2:])
	case "新しく", "renew":
		err = cmdRenew(os.Args[2:])
	case "tree", "木":
		err = cmdTree()
	case "設定そろえ", "align-settings":
		err = cmdSettingsAlign(os.Args[2:])
	case "チャネル準備", "channel-setup":
		err = cmdChannelSetup()
	case "run":
		dry := len(os.Args) > 2 && os.Args[2] == "--dry"
		err = cmdRun(dry)
	case "更新", "update":
		err = cmdUpdate(os.Args[2:])
	case "設定項目", "settings":
		err = cmdSettings(len(os.Args) > 2 && os.Args[2] == "--dry")
	case "届ける", "send":
		err = cmdDeliver(os.Args[2:], os.Stdin)
	case "version", "--version", "-v", "版":
		fmt.Println(version())
		return
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "知らないコマンドです: %s\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsumugi:", err)
		os.Exit(1)
	}
}

func cmdStatus() error {
	root, err := homeDir()
	if err != nil {
		return err
	}
	affs, err := loadAffiliations(root)
	if err != nil {
		return err
	}
	as, err := listAgents()
	if err != nil {
		return err
	}
	if len(affs) == 0 {
		fmt.Printf("所属がまだありません（%s/<所属>/%s を置くと出ます）\n\n", root, configFile)
	}
	printStatus(os.Stdout, compare(affs, as))
	return nil
}

func cmdRun(dry bool) error {
	root, err := homeDir()
	if err != nil {
		return err
	}
	affs, err := loadAffiliations(root)
	if err != nil {
		return err
	}
	if !dry {
		unlock, err := lockHome(root, 2*time.Minute)
		if err != nil {
			return err
		}
		err = reconcile(os.Stdout, affs, dry)
		unlock()
		if err != nil {
			return err
		}
	} else if err := reconcile(os.Stdout, affs, dry); err != nil {
		return err
	}
	// 更新は run の錠の外で、別の錠を取って回す（長い更新の間も、ほかの run が起こす・届けるを続けられる）。
	if g, err := loadGlobal(root); err == nil {
		maybeUpdate(os.Stdout, root, g, time.Now(), dry)
	}
	return nil
}

func cmdList() error {
	root, err := homeDir()
	if err != nil {
		return err
	}
	affs, err := loadAffiliations(root)
	if err != nil {
		return err
	}
	learnAll(affs)
	all, err := listAllAgents()
	if err != nil {
		return err
	}
	printList(os.Stdout, affs, all)
	return nil
}
