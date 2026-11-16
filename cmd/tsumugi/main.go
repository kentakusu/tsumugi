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
  list, l             紡のセッションを番号・名前で並べる
  attach, a <名前|番号|ID>
                      開く（止まっていれば同じ会話のまま起こしてから）
  届ける, send <名前> [指示…]
                      セッションへ指示を渡す（無ければ標準入力を 1 件として。すぐ渡せなければ run が渡す）
  更新, update [--dry] Claude Code と OS などを新しくする（紡.json の「更新」のとおり。無ければ claude・OS・設定項目）
  設定項目, settings [--dry]
                      Claude Code の設定項目の一覧を取り、前回からの増減と、手元の設定の「一覧に無い鍵」を出す
  yui <問い合わせ>…    結のデーモンに名乗って問い合わせる（無ければ標準入力を 1 件として）
  install [--dry]     OS の定期実行に run を登録する（--dry は書くものを見るだけ）
  uninstall           その登録を外す

  version, --version   版（git の commit）を出す

所属の設定は $TSUMUGI_HOME/<所属>/所属.json（TSUMUGI_HOME が無ければ ~/.tsumugi）。
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
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
	case "run":
		dry := len(os.Args) > 2 && os.Args[2] == "--dry"
		err = cmdRun(dry)
	case "更新", "update":
		err = cmdUpdate(len(os.Args) > 2 && os.Args[2] == "--dry")
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
		defer unlock()
	}
	return reconcile(os.Stdout, affs, dry)
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
	all, err := listAllAgents()
	if err != nil {
		return err
	}
	printList(os.Stdout, affs, all)
	return nil
}
