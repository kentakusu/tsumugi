// tsumugi — Claude Code を所属ごとに無人で常駐させる。
//
// いま在るのは status だけ（run・install はこれから）。
package main

import (
	"fmt"
	"os"
)

const usage = `使い方: tsumugi <コマンド>

  status   所属ごとに、望む状態（所属.json）と実際（claude agents）を並べる

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
