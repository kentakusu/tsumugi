package main

// 紡の自己更新: GitHub の Release（main に push するたびに Actions が作る）から、
// このマシンの OS・CPU 向けの実行ファイルを取り、SHA256SUMS で確かめてから置き換える。
//
// 置き換えるのは「Release のほうが新しい」ときだけ（commit の時刻で比べる）。
// 手元でビルドした新しいもの（まだ Release が無い）を、古い Release で戻さないため。
// ★手元の変更が入ったビルド（vcs.modified）は置き換えない（開発中のものを守る）。

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

const releaseAPI = "https://api.github.com/repos/kentakusu/tsumugi/releases/latest"

type buildInfo struct {
	Rev   string
	Time  time.Time
	Dirty bool
}

func ownBuild() buildInfo {
	var b buildInfo
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				b.Rev = s.Value
			case "vcs.time":
				b.Time, _ = time.Parse(time.RFC3339, s.Value)
			case "vcs.modified":
				b.Dirty = s.Value == "true"
			}
		}
	}
	return b
}

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// タグ r20261117-224316-b842d0d から、commit の時刻と短い ID を読む。
func parseTag(tag string) (time.Time, string, bool) {
	parts := strings.Split(strings.TrimPrefix(tag, "r"), "-")
	if len(parts) != 3 {
		return time.Time{}, "", false
	}
	t, err := time.Parse("20060102150405", parts[0]+parts[1])
	if err != nil {
		return time.Time{}, "", false
	}
	return t, parts[2], true
}

func assetName() string {
	n := "tsumugi_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

var httpClient = &http.Client{Timeout: 2 * time.Minute}

func httpGet(url string) ([]byte, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "tsumugi-selfupdate")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s が %s を返しました", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// 自己更新する。置き換えたら新しい版の名前を返す（変わらなければ空）。
func selfUpdate(w io.Writer, dry bool) (string, error) {
	cleanupOld()
	own := ownBuild()
	if own.Dirty {
		fmt.Fprintln(w, "紡: 手元の変更が入ったビルドなので置き換えない")
		return "", nil
	}
	b, err := httpGet(releaseAPI)
	if err != nil {
		return "", err
	}
	var r release
	if err := json.Unmarshal(b, &r); err != nil {
		return "", fmt.Errorf("Release を読めません: %w", err)
	}
	at, short, ok := parseTag(r.Tag)
	if !ok {
		return "", fmt.Errorf("Release のタグ %q を読めません", r.Tag)
	}
	if strings.HasPrefix(own.Rev, short) || (!own.Time.IsZero() && !at.After(own.Time)) {
		fmt.Fprintf(w, "紡: 最新です（%s）\n", version())
		return "", nil
	}
	var binURL, sumURL string
	for _, a := range r.Assets {
		switch a.Name {
		case assetName():
			binURL = a.URL
		case "SHA256SUMS":
			sumURL = a.URL
		}
	}
	if binURL == "" || sumURL == "" {
		return "", fmt.Errorf("Release %s に %s か SHA256SUMS がありません", r.Tag, assetName())
	}
	fmt.Fprintf(w, "紡: %s → %s\n", version(), r.Tag)
	if dry {
		return r.Tag, nil
	}
	sums, err := httpGet(sumURL)
	if err != nil {
		return "", err
	}
	want := ""
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == assetName() {
			want = f[0]
		}
	}
	if want == "" {
		return "", fmt.Errorf("SHA256SUMS に %s がありません", assetName())
	}
	bin, err := httpGet(binURL)
	if err != nil {
		return "", err
	}
	got := sha256.Sum256(bin)
	if hex.EncodeToString(got[:]) != want {
		return "", fmt.Errorf("★%s の SHA256 が合いません（置き換えない）", assetName())
	}
	if err := replaceSelf(bin); err != nil {
		return "", err
	}
	return r.Tag, nil
}

func selfPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p, nil
}

// 置き換える。Windows は動いている exe を上書きできないが、名前は変えられるので、
// いまのものを .old に退けてから新しいものを置く（.old は次に動いたときに消す）。
func replaceSelf(bin []byte) error {
	p, err := selfPath()
	if err != nil {
		return err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	tmp := p + ".new"
	if err := os.WriteFile(tmp, bin, fi.Mode().Perm()|0o100); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		old := p + ".old"
		os.Remove(old)
		if err := os.Rename(p, old); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, p); err != nil {
			os.Rename(old, p) // 戻す
			return err
		}
		return nil
	}
	return os.Rename(tmp, p)
}

func cleanupOld() {
	if p, err := selfPath(); err == nil {
		os.Remove(p + ".old")
	}
}
