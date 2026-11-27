package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/task"
)

// calendarsFile is the list of calendars lacia serve reads when the settings
// name no other.
const calendarsFile = ".lacia/calendars.json"

// profileName is the line of memory/profile.md that carries the name, as
// the template leaves it.
const profileName = "- 呼び名："

// runInit writes the name Lacia calls her owner by into a task repository
// copied from the template: the settings, and the profile when its line is
// still empty. It also offers to name the Google account whose calendar is
// read. It changes nothing else, and may be run again.
func runInit(args []string) error {
	fs := commandFlags("init")
	rootFlag := fs.String("root", "", "記録のリポジトリー（省略すると、いまのディレクトリーから探します）")
	user := fs.String("user", "", "わたしに呼んでほしい名前（例: 田中さん）。省くと、このコマンドの実行中に尋ねます")
	account := fs.String("calendar", "", "予定を読む Google アカウントのメールアドレス。カレンダーが未設定で --user と --calendar の両方を省いたときは、呼び名に続いて尋ねます")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// With --user alone nothing is asked, as before.
	ask := *user == ""
	stdin := bufio.NewReader(os.Stdin)
	root, err := findRoot(*rootFlag)
	if err != nil {
		return err
	}
	if readOnlyCopy(root) {
		return errors.New("ここは記録の読み取り専用のコピーです。lacia serve が記録を書き込むリポジトリーで lacia init を実行してください")
	}
	if err := requireCurrentRecords(root); err != nil {
		return err
	}
	path := filepath.Join(root, task.ConfigFile)
	settings := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("%s を読めません: %w", task.ConfigFile, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	current := ""
	json.Unmarshal(settings["user"], &current)

	name := strings.TrimSpace(*user)
	if name == "" {
		hint := ""
		if current != "" {
			hint = "いまの呼び名は「" + current + "」です。そのまま使うときは、何も入力せずに Enter を押してください。"
		}
		fmt.Fprintf(os.Stderr, "わたしに呼んでほしい名前を入力してください（例: 田中さん）。%s\n> ", hint)
		line, err := stdin.ReadString('\n')
		if name = strings.TrimSpace(line); name == "" {
			if err != nil && current == "" {
				return errors.New("名前を読み取れませんでした。lacia init --user <名前> で指定してください")
			}
			name = current
		}
	}
	if name == "" {
		return errors.New("わたしに呼んでほしい名前を入力してください")
	}
	if strings.ContainsAny(name, "\n\r\t{}") || len([]rune(name)) > 40 {
		return errors.New("呼び名は40字以内で、改行、タブ、波かっこを含めずに書いてください")
	}

	if name != current {
		settings["user"], _ = json.Marshal(name)
		data, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Printf("%s の user を「%s」にしました。\n", task.ConfigFile, name)
	} else {
		fmt.Printf("呼び名は「%s」のままです。\n", name)
	}

	// The profile is the owner's and Lacia's to write: only the empty line
	// the template leaves is filled in.
	profile := filepath.Join(root, "memory", "profile.md")
	if data, err := os.ReadFile(profile); err == nil {
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.TrimSpace(line) == profileName {
				lines[i] = profileName + name
				if err := os.WriteFile(profile, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
					return err
				}
				fmt.Println("memory/profile.md に呼び名を書きました。")
				break
			}
		}
	}
	if err := initCalendar(root, strings.TrimSpace(*account), ask, stdin); err != nil {
		return err
	}
	fmt.Println("lacia codex か lacia claude で、わたしと会話を始められます。")
	return nil
}

// initCalendar writes the list of calendars with the primary calendar of one
// account. A list that is already there is the owner's, and is left alone.
func initCalendar(root, account string, ask bool, stdin *bufio.Reader) error {
	path := filepath.Join(root, calendarsFile)
	if fileExists(path) {
		if account != "" {
			return fmt.Errorf("%s が、すでにあります。読むカレンダーを変えるときは、このファイルを直してください", calendarsFile)
		}
		return nil
	}
	if account == "" && ask {
		fmt.Fprint(os.Stderr, "予定を読む Google アカウントのメールアドレスを入力してください。予定を読まないときは、何も入力せずに Enter を押してください。\n> ")
		line, _ := stdin.ReadString('\n')
		account = strings.TrimSpace(line)
	}
	if account == "" {
		return nil
	}
	at := strings.Index(account, "@")
	if at < 1 || at == len(account)-1 || strings.ContainsAny(account, " \t\r\n\"\\<>") {
		return errors.New("メールアドレスの形で入力してください（例: tanaka@example.com）")
	}
	data, err := json.MarshalIndent([]map[string]string{{"account": account, "calendar_id": "primary", "label": "予定"}}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s に、%s の予定を読む設定を書きました。\n", calendarsFile, account)
	fmt.Println("Google との連携を設定した lacia serve を起動してください。すでに動いているときは起動し直します。そのあと Web の「予定の連携」で、Google アカウントとの連携を許可してください。")
	return nil
}
