package main

// 紡が起こしたセッションの控え（<所属>/状態.json）。
//
// 名前 → 会話（セッション ID）。止まったものを起こし直すとき、同じ会話を続けるために使う。
// 紡が自分で書くファイルなので、手で書かない。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const stateFile = "状態.json"

type sessionState struct {
	SessionID string `json:"会話"`
	// 定時ごとに、最後に届けた（または諦めた）発火時刻。
	Delivered map[string]string `json:"定時,omitempty"`
}

type affState struct {
	Sessions map[string]sessionState `json:"セッション"`
}

func statePath(a affiliation) string { return filepath.Join(filepath.Dir(a.Path), stateFile) }

func loadState(a affiliation) (affState, error) {
	st := affState{Sessions: map[string]sessionState{}}
	b, err := os.ReadFile(statePath(a))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("%s を読めません: %w", statePath(a), err)
	}
	if st.Sessions == nil {
		st.Sessions = map[string]sessionState{}
	}
	return st, nil
}

// 書きかけで落ちても壊れないよう、別名に書いてから置き換える。
func saveState(a affiliation, st affState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	p := statePath(a)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
