// tsumugi — Claude Code を所属ごとに無人で常駐させる。
package main

import (
	"fmt"
	"os"
)

const usage = `使い方: tsumugi <コマンド>

  status        所属ごとに、望む状態（所属.json）と実際（claude agents）を並べる
  run           望む状態に寄せる（足りないものを起こし、紡が起こして要らなくなったものを止める）
  run --dry     やることを出すだけで、何もしない
  list          紡のセッションを名前で並べる
  attach <名前>  名前で開く（止まっていれば同じ会話のまま起こしてから）
  install       OS の定期実行に run を登録する（--dry で書くものを見るだけ）
  uninstall     その登録を外す

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
	case "list":
		err = cmdList()
	case "attach":
		if len(os.Args) < 3 {
			fmt.Fprint(os.Stderr, "使い方: tsumugi attach <名前>\n")
			os.Exit(2)
		}
		err = cmdAttach(os.Args[2])
	case "install", "uninstall":
		dry := len(os.Args) > 2 && os.Args[2] == "--dry"
		err = cmdInstall(dry, os.Args[1] == "uninstall")
	case "run":
		dry := len(os.Args) > 2 && os.Args[2] == "--dry"
		err = cmdRun(dry)
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
