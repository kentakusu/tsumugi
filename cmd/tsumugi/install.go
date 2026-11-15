package main

// OS の定期実行に「tsumugi run」を登録する。
//
// 紡は自前で常駐しない。run は何度流しても同じ結果になるので、OS に数分おきに
// 呼ばせれば「落ちたら起こし直す」が成り立つ。紡自身の死活は OS が見る。
//
//   Linux   systemd の user timer
//   macOS   launchd の LaunchAgent
//   Windows タスク スケジューラ

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	unitName     = "tsumugi"
	launchdLabel = "io.github.kentakusu.tsumugi"
	interval     = 5 // 分
)

// 登録に要るもの。
type installEnv struct {
	Exe     string // tsumugi 本体の絶対パス
	Path    string // 登録したときの PATH（claude と、セッションが使う node 等を見つけるため）
	Home    string // TSUMUGI_HOME（指定があれば）
	LogFile string
}

// 書くファイル 1 つ。
type file struct {
	Path, Body string
}

func currentInstallEnv() (installEnv, error) {
	exe, err := os.Executable()
	if err != nil {
		return installEnv{}, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return installEnv{}, err
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return installEnv{}, fmt.Errorf("PATH に claude が在りません。claude が使えるシェルから install してください")
	}
	root, err := homeDir()
	if err != nil {
		return installEnv{}, err
	}
	return installEnv{Exe: exe, Path: os.Getenv("PATH"), Home: os.Getenv("TSUMUGI_HOME"),
		LogFile: filepath.Join(root, "run.log")}, nil
}

// systemd: run を 1 回流す service と、それを 5 分おきに呼ぶ timer。
func systemdFiles(dir string, e installEnv) []file {
	env := fmt.Sprintf("Environment=%s\n", systemdQuote("PATH="+e.Path))
	if e.Home != "" {
		env += fmt.Sprintf("Environment=%s\n", systemdQuote("TSUMUGI_HOME="+e.Home))
	}
	service := fmt.Sprintf(`# tsumugi install が書いた。手で直さず、tsumugi install をやり直す。
[Unit]
Description=tsumugi run（所属.json のとおりに Claude Code のセッションを揃える）

[Service]
Type=oneshot
ExecStart=%s run
%s# run が起こした claude の supervisor とセッションは、run が終わっても生かす。
# （既定の control-group だと、oneshot の終わりに一緒に殺される）
KillMode=process
`, systemdQuote(e.Exe), env)
	timer := fmt.Sprintf(`# tsumugi install が書いた。
[Unit]
Description=tsumugi run を %d 分おきに

[Timer]
OnBootSec=1min
OnUnitInactiveSec=%dmin

[Install]
WantedBy=timers.target
`, interval, interval)
	return []file{
		{filepath.Join(dir, unitName+".service"), service},
		{filepath.Join(dir, unitName+".timer"), timer},
	}
}

func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// launchd: 5 分おき・ログイン時に run。
func launchdFile(dir string, e installEnv) file {
	x := html.EscapeString
	env := fmt.Sprintf("\t\t<key>PATH</key><string>%s</string>\n", x(e.Path))
	if e.Home != "" {
		env += fmt.Sprintf("\t\t<key>TSUMUGI_HOME</key><string>%s</string>\n", x(e.Home))
	}
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- tsumugi install が書いた。 -->
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>run</string></array>
	<key>EnvironmentVariables</key>
	<dict>
%s	</dict>
	<key>StartInterval</key><integer>%d</integer>
	<key>RunAtLoad</key><true/>
	<key>AbandonProcessGroup</key><true/>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, launchdLabel, x(e.Exe), env, interval*60, x(e.LogFile), x(e.LogFile))
	return file{filepath.Join(dir, launchdLabel+".plist"), body}
}

// タスク スケジューラ: 5 分おきに run。PATH はタスクに持たせられないので、ユーザーの PATH が使われる。
func schtasksArgs(e installEnv) []string {
	return []string{"/Create", "/F", "/TN", unitName, "/SC", "MINUTE", "/MO", fmt.Sprint(interval),
		"/TR", fmt.Sprintf(`"%s" run`, e.Exe)}
}

func cmdInstall(dry, remove bool) error {
	e, err := currentInstallEnv()
	if err != nil && !remove {
		return err
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "linux":
		dir := filepath.Join(home, ".config", "systemd", "user")
		fs := systemdFiles(dir, e)
		if remove {
			return uninstallSteps(dry, fs, [][]string{
				{"systemctl", "--user", "disable", "--now", unitName + ".timer"},
			}, [][]string{{"systemctl", "--user", "daemon-reload"}})
		}
		return installSteps(dry, fs, [][]string{
			{"systemctl", "--user", "daemon-reload"},
			{"systemctl", "--user", "enable", "--now", unitName + ".timer"},
		}, "ログアウト中も動かすには、一度だけ: loginctl enable-linger")
	case "darwin":
		f := launchdFile(filepath.Join(home, "Library", "LaunchAgents"), e)
		target := fmt.Sprintf("gui/%d", os.Getuid())
		if remove {
			return uninstallSteps(dry, []file{f}, [][]string{{"launchctl", "bootout", target + "/" + launchdLabel}}, nil)
		}
		return installSteps(dry, []file{f}, [][]string{
			{"launchctl", "bootout", target + "/" + launchdLabel}, // 入れ直しのため。初回は失敗してよい
			{"launchctl", "bootstrap", target, f.Path},
		}, "")
	case "windows":
		if remove {
			return uninstallSteps(dry, nil, [][]string{{"schtasks", "/Delete", "/F", "/TN", unitName}}, nil)
		}
		return installSteps(dry, nil, [][]string{append([]string{"schtasks"}, schtasksArgs(e)...)}, "")
	}
	return fmt.Errorf("%s には、まだ対応していません", runtime.GOOS)
}

func installSteps(dry bool, fs []file, cmds [][]string, note string) error {
	for _, f := range fs {
		if dry {
			fmt.Printf("── %s ──\n%s\n", f.Path, f.Body)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f.Path, []byte(f.Body), 0o644); err != nil {
			return err
		}
		fmt.Println("書いた:", f.Path)
	}
	for i, c := range cmds {
		if dry {
			fmt.Printf("（しない）%q\n", c)
			continue
		}
		out, err := exec.Command(c[0], c[1:]...).CombinedOutput()
		if err != nil && !(runtime.GOOS == "darwin" && i == 0) {
			return fmt.Errorf("%q が失敗しました: %w: %s", c, err, out)
		}
		fmt.Printf("した: %q\n", c)
	}
	if !dry {
		fmt.Printf("登録しました。%d 分おきに tsumugi run が流れます。\n", interval)
		if note != "" {
			fmt.Println(note)
		}
	}
	return nil
}

func uninstallSteps(dry bool, fs []file, before, after [][]string) error {
	run := func(cmds [][]string) {
		for _, c := range cmds {
			if dry {
				fmt.Printf("（しない）%q\n", c)
				continue
			}
			if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
				fmt.Printf("%q: %v %s\n", c, err, strings.TrimSpace(string(out)))
			}
		}
	}
	run(before)
	for _, f := range fs {
		if dry {
			fmt.Println("（しない）消す:", f.Path)
			continue
		}
		if err := os.Remove(f.Path); err == nil {
			fmt.Println("消した:", f.Path)
		}
	}
	run(after)
	if !dry {
		fmt.Println("登録を外しました。動いているセッションはそのままです。")
	}
	return nil
}
