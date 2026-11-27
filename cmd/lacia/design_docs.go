package main

import (
	"fmt"
	"os"

	"github.com/imos/lacia"
)

func runDesignDocs(args []string) error {
	name := ""
	switch len(args) {
	case 1:
		name = args[0]
		if name == "--help" || name == "-h" {
			fmt.Println("使い方: lacia docs [名前]。名前を省くと設計文書の一覧を出します。")
			return nil
		}
	case 0:
	default:
		return fmt.Errorf("設計文書の名前を1つ指定してください。lacia docs で一覧を読めます")
	}
	_, content, err := lacia.DesignDocument(name)
	if err != nil {
		return fmt.Errorf("設計文書 %q は見つかりません。lacia docs で一覧を読めます", name)
	}
	_, err = os.Stdout.Write(content)
	return err
}
