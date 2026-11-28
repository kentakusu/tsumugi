package main

// macOS: 紡の実行ファイルに決まった識別子で署名する（2026-11-28 yumeyume-main が Mac mini で実測）。
//
// ★macOS の「ローカルネットワーク」の許可は、実行ファイルの署名の識別子に付く。Go が作る実行ファイルは
// linker の ad-hoc 署名で識別子が a.out なので、確認の画面が出ず、設定の一覧にも出ない＝許可できない。
// そのまま LAN の結へつなぐと connect: no route to host（EHOSTUNREACH）になる。
// codesign -f -s - --identifier io.github.kentakusu.tsumugi で署名し直すと、許可を一度押せば以後は届く。
// 許可は識別子に付く（cdhash ではない）ので、自己更新のたびに同じ識別子で署名し直せば、押し直しは要らない。
// 署名し直さずに入れ替えると a.out に戻り、また届かなくなる。

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"syscall"
)

const macSignID = "io.github.kentakusu.tsumugi"

// path を macOS の決まった識別子で署名する。macOS 以外では何もしない。テストで差し替える。
var signForMac = func(path string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	out, err := exec.Command("codesign", "-f", "-s", "-", "--identifier", macSignID, path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign が失敗しました: %v: %s", err, out)
	}
	return nil
}

// LAN へつなげなかったときに、macOS のローカルネットワークの許可が無い見込みなら、直し方を返す。
func macLocalNetworkHint(goos string, err error) string {
	if goos != "darwin" || !errors.Is(err, syscall.EHOSTUNREACH) {
		return ""
	}
	return "\n★macOS で no route to host なら、「ローカルネットワーク」の許可が無い見込みです。" +
		"紡の実行ファイルの署名の識別子が " + macSignID + " か確かめ（codesign -dv ~/.local/bin/tsumugi）、" +
		"違えば codesign -f -s - --identifier " + macSignID + " ~/.local/bin/tsumugi で署名し直してください。" +
		"一度も許可していなければ、システム設定 → プライバシーとセキュリティ → ローカルネットワーク で許可します（README の macOS の節）"
}
