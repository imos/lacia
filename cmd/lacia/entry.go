package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"

	"github.com/imos/lacia/internal/effect/program"
)

// runOpen opens the home page of the web, or the page of a task, in the
// browser.
func runOpen(args []string) error {
	page, err := openPage(args)
	if err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("この端末では、ブラウザーを開けません。%s をブラウザーで開いてください", page)
	}
	_, err = program.Open().Output(context.Background(), "", page)
	return err
}

// openPage is the address lacia open opens: the server the other commands
// use, and the page of the task if one is named.
func openPage(args []string) (string, error) {
	if len(args) > 1 || len(args) == 1 && strings.HasPrefix(args[0], "-") {
		return "", usageError("open")
	}
	root, _ := findRoot("")
	base := string(apiForRoot(root))
	if base == "" {
		return "", errors.New(apiAddressHelp)
	}
	if len(args) == 1 {
		return base + "/tasks/" + url.PathEscape(args[0]), nil
	}
	return base + "/", nil
}

func runRoot(args []string) error {
	if len(args) > 0 {
		return usageError("root")
	}
	root, err := findRoot("")
	if err != nil {
		return err
	}
	fmt.Println(root)
	return nil
}

// runShell becomes the owner's shell in the records. When it ends, the
// shell lacia was run from is still where it was.
func runShell(args []string) error {
	if len(args) > 0 {
		return usageError("shell")
	}
	root, err := findRoot("")
	if err != nil {
		return err
	}
	if err := os.Chdir(root); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "記録の場所（%s）で、シェルを開きます。exit で、元の場所に戻ります。\n", root)
	return program.Shell().Replace(nil, lastWins(append(os.Environ(), "PWD="+root)))
}
