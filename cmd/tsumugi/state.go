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
	"time"
)

const stateFile = "状態.json"

type sessionState struct {
	SessionID string `json:"会話"`
	// 定時ごとに、最後に届けた（または諦めた）発火時刻。
	Delivered map[string]string `json:"定時,omitempty"`
	// 見張り: いまの様子と、いつから続いているか。
	Watch *watchState `json:"見張り,omitempty"`
	// 前の会話: 紡がこのセッションとして控えていた、いまより前の会話 ID（新しく立て直した・写しになった等）。
	// tsumugi tree が並べ、claude agents の Completed に残っていれば紡が claude rm で片付ける（lineage.go）。
	Previous []prevSession `json:"前の会話,omitempty"`
}

type prevSession struct {
	ID    string `json:"会話"`
	Until string `json:"まで"` // 乗り換えた時刻
	Why   string `json:"わけ,omitempty"`
}

// 控えの会話を newID に乗り換える。前の会話は「前の会話」に残す。乗り換えたら true。
func (ss *sessionState) moveTo(newID, why string) bool {
	if newID == "" || ss.SessionID == newID {
		return false
	}
	if ss.SessionID != "" {
		ss.Previous = append(ss.Previous, prevSession{ID: ss.SessionID, Until: time.Now().Format(time.RFC3339), Why: why})
		if len(ss.Previous) > 50 { // 増え続けないように古いものから捨てる
			ss.Previous = ss.Previous[len(ss.Previous)-50:]
		}
	}
	ss.SessionID = newID
	return true
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

// 控えた会話 ID を覚える（読めないものは飛ばす）。
func learnAll(affs []affiliation) {
	states := map[string]affState{}
	for _, af := range affs {
		if st, err := loadState(af); err == nil {
			states[af.ID] = st
		}
	}
	learnIDs(states)
}
