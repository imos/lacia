package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/imos/lacia/internal/documents"
)

func runDocuments(kind string, args []string) error {
	if len(args) == 0 || args[0] != "list" {
		return fmt.Errorf("%s のあとに list を指定してください", kind)
	}
	fs := commandFlags(kind + " list")
	root := fs.String("root", "", "記録のリポジトリー")
	area := fs.String("area", "", "領域")
	asJSON := fs.Bool("json", false, "JSONで出します")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	dir, err := findRoot(*root)
	if err != nil {
		return err
	}
	docs, err := documents.List(dir, kind, *area)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(docs)
	}
	for _, doc := range docs {
		fmt.Printf("%s  %s\n  読むとき: %s  更新日: %s\n", doc.Path, doc.Title, doc.When, doc.Updated)
	}
	return nil
}
