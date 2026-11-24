package main

import (
	"io"
	"os"
	"testing"
)

// 本物のクラウドのセッションを 1 本立てる（使い捨て）。TSUMUGI_CLOUD_LIVE に git のリポジトリの道を入れたときだけ動く。
func TestCloudCreateLive(t *testing.T) {
	dir := os.Getenv("TSUMUGI_CLOUD_LIVE")
	if dir == "" {
		t.Skip("TSUMUGI_CLOUD_LIVE が無い")
	}
	if err := trustBeforeStart(io.Discard, action{Affiliation: "試し", Name: "試し", Dir: dir}, false); err != nil {
		t.Fatal(err)
	}
	id, err := cloudCreate(dir, "【紡の試し・Go の疑似端末から】これは使い捨てのクラウドのセッションです。README.md を 1 行で要約し、そのあとは何もせず待ってください。")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("立てた: %s", id)
	if err := cloudSend(id, "【紡の試し・2 通目（Go から）】受け取ったら「Go からの 2 通目を受け取りました」とだけ返して待ってください。"); err != nil {
		t.Fatal(err)
	}
	t.Logf("積んだ: %s", id)
}
