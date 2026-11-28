package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/effect/program"
	"github.com/imos/lacia/internal/home"
	"github.com/imos/lacia/internal/starter"
	"github.com/imos/lacia/internal/task"
)

const reachHelp = `リポジトリー %s を読めませんでした。URL と、この端末から GitHub へつなぐ設定を確かめてください。
GitHub へつなぐ設定がまだなら、次の2つを実行してから、HTTPS の URL（https://github.com/<名前>/<リポジトリー>.git）でやり直してください。
  gh auth login
  gh auth setup-git
%w`

// setupRecords makes the records of this machine from the owner's
// repository and returns where they are. An empty repository gets the
// first content and a first push; one that holds records is cloned as it
// is; anything else is left alone.
func setupRecords(ctx context.Context, o *setupOptions, a *asker) (string, error) {
	var err error
	if o.repo == "" {
		if o.repo, err = a.ask("記録を置く GitHub のリポジトリーの URL を入力してください。空の、非公開のリポジトリーです（例: https://github.com/tanaka/tanaka-lacia.git）", "", "--repo"); err != nil {
			return "", err
		}
	}
	refs, err := program.Git().Output(ctx, "", "ls-remote", "--", o.repo)
	if err != nil {
		return "", fmt.Errorf(reachHelp, o.repo, err)
	}
	if public(ctx, o.repo) {
		return "", fmt.Errorf("リポジトリー %s は、だれでも読める公開のリポジトリーです。記録には個人の情報が入るので、非公開にしてからやり直してください", o.repo)
	}
	if o.dir == "" {
		def := filepath.Join(os.Getenv("HOME"), repoName(o.repo))
		if o.dir, err = a.ask("記録を置く場所を入力してください。", def, "--dir"); err != nil {
			return "", err
		}
	}
	dir, err := filepath.Abs(resolvePath("", o.dir))
	if err != nil {
		return "", err
	}
	if err := allowedPlace(dir); err != nil {
		return "", err
	}
	if existing, err := sameClone(ctx, dir, o.repo); existing || err != nil {
		return dir, err
	}
	empty := strings.TrimSpace(string(refs)) == ""
	if !empty {
		if err := requireRecords(ctx, o.repo); err != nil {
			return "", err
		}
	}
	if _, err := program.Git().Output(ctx, "", "clone", "--quiet", "--", o.repo, dir); err != nil {
		return "", err
	}
	fmt.Printf("リポジトリーを %s にクローンしました。\n", dir)
	if empty {
		return dir, firstRecords(ctx, dir, o, a)
	}
	return dir, nil
}

// repoName is the last part of the URL, without .git.
func repoName(repo string) string {
	repo = strings.TrimSuffix(strings.TrimRight(repo, "/"), ".git")
	if i := strings.LastIndexAny(repo, "/:"); i >= 0 {
		repo = repo[i+1:]
	}
	return repo
}

// githubSSH is the SSH form of a GitHub URL, which is read anonymously
// through its HTTPS form.
var githubSSH = regexp.MustCompile(`^(?:ssh://)?git@github\.com[:/](.+)$`)

// public reports whether the repository can be read with no login at all.
// Only GitHub is asked: elsewhere, and for a path on this machine, there is
// no form to read anonymously.
func public(ctx context.Context, repo string) bool {
	url := repo
	if m := githubSSH.FindStringSubmatch(repo); m != nil {
		url = "https://github.com/" + m[1]
	}
	if !strings.HasPrefix(url, "https://github.com/") {
		return false
	}
	cmd := program.Git().Command(ctx, "-c", "credential.helper=", "ls-remote", "--", url, "HEAD")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ASKPASS=/usr/bin/false", "SSH_ASKPASS=/usr/bin/false")
	return cmd.Run() == nil
}

// allowedPlace refuses the places macOS guards or iCloud syncs: a binary
// signed ad hoc is a new program to macOS after each update and would be
// asked for permission again, and a repository that iCloud and the server
// both write is known to break.
func allowedPlace(dir string) error {
	h := os.Getenv("HOME")
	if dir == h || within(home.Dir(os.Getenv), dir) {
		return fmt.Errorf("%s には、記録を置けません。ホームの下に、記録のためのフォルダーを指定してください", dir)
	}
	for _, guarded := range []string{"Documents", "Desktop", "Downloads", "Library/Mobile Documents"} {
		if within(filepath.Join(h, guarded), dir) {
			return fmt.Errorf("%s の下には、記録を置けません。macOS が許可を求め直す場所か、iCloud が同期する場所だからです。ホームの直下などを指定してください", filepath.Join(h, guarded))
		}
	}
	return nil
}

// sameClone reports whether dir is already a clone of repo, to be used as
// it is. A directory with anything else in it is refused; an empty one is
// left for the clone.
func sameClone(ctx context.Context, dir, repo string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) || err == nil && len(entries) == 0 {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	origin, err := program.Git().Output(ctx, dir, "remote", "get-url", "origin")
	if err == nil && strings.TrimSuffix(strings.TrimSpace(string(origin)), ".git") == strings.TrimSuffix(repo, ".git") {
		fmt.Printf("%s にある、同じリポジトリーのクローンを使います。\n", dir)
		return true, nil
	}
	return false, fmt.Errorf("%s には、別のものがあります。空のフォルダーか、まだない場所を --dir で指定してください", dir)
}

// requireRecords looks into a repository that is not empty, through a
// shallow clone in the tools directory, and accepts only Lacia's records.
func requireRecords(ctx context.Context, repo string) error {
	tools, err := disk.OpenTools(home.Dir(os.Getenv))
	if err != nil {
		return err
	}
	defer tools.Close()
	const look = "tmp/setup-look"
	if err := tools.RemoveAll(look); err != nil {
		return err
	}
	defer tools.RemoveAll(look)
	dir := filepath.Join(tools.Path(), look)
	if _, err := program.Git().Output(ctx, "", "clone", "--quiet", "--depth=1", "--no-checkout", "--", repo, dir); err != nil {
		return err
	}
	if _, err := program.Git().Output(ctx, dir, "cat-file", "-e", "HEAD:"+task.ConfigFile); err != nil {
		return fmt.Errorf("リポジトリー %s には、レイシアの記録ではないものが入っています。新しく作った、空のリポジトリーを指定してください", repo)
	}
	fmt.Println("リポジトリーにあるレイシアの記録を、そのまま使います。")
	return nil
}

// firstRecords writes the first content into the clone of an empty
// repository and pushes it.
func firstRecords(ctx context.Context, dir string, o *setupOptions, a *asker) error {
	if o.name == "" {
		var err error
		if o.name, err = a.ask("わたしに呼んでほしい名前を入力してください（例: 田中さん）。", "", "--name"); err != nil {
			return err
		}
	}
	if strings.ContainsAny(o.name, "\n\r\t{}\"\\") || len([]rune(o.name)) > 40 {
		return errors.New("呼び名は40字以内で、改行、タブ、波かっこ、引用符を含めずに書いてください")
	}
	records, err := disk.OpenRecords(dir)
	if err != nil {
		return err
	}
	defer records.Close()
	if err := starter.Write(records, o.name); err != nil {
		return err
	}
	git := func(args ...string) error {
		_, err := program.Git().Output(ctx, dir, args...)
		return err
	}
	if err := git("symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return err
	}
	// The server commits on its own, so the records need someone to
	// commit as even on a machine where Git was never told a name.
	if git("var", "GIT_COMMITTER_IDENT") != nil {
		if err := git("config", "user.name", "Lacia"); err != nil {
			return err
		}
		if err := git("config", "user.email", "lacia@localhost"); err != nil {
			return err
		}
		fmt.Println("Git に名前とメールアドレスが設定されていなかったので、この記録のコミットには「Lacia <lacia@localhost>」を使います。")
	}
	for _, args := range [][]string{{"add", "--all"}, {"commit", "--quiet", "-m", "記録を始める"}, {"push", "--quiet", "-u", "origin", "main"}} {
		if err := git(args...); err != nil {
			return err
		}
	}
	fmt.Println("記録の初期の中身を置いて、プッシュしました。")
	return nil
}
