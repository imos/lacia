package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/imos/lacia/internal/rules"
	"github.com/imos/lacia/internal/task"
)

// rulesFor finds the repository the rules are read for and the name its
// owner is called by. Without a repository the embedded rules still print.
func rulesFor() (root, owner string) {
	root, _ = findRoot("")
	var cfg task.Config
	if root != "" {
		cfg, _ = task.LoadConfig(root)
	}
	return root, cfg.OwnerName()
}

// runRules prints the rules a conversation follows. They travel inside the
// binary; the repository may replace or extend them under rules/.
func runRules(args []string) error {
	root, owner := rulesFor()
	if len(args) > 1 {
		return usageError("rules")
	}
	if len(args) == 1 && args[0] == "--session-json" {
		current, err := rules.ReadSessionRules(root, owner)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(current)
	}
	name := rules.Core
	if len(args) == 1 {
		name = args[0]
	}
	if name == "list" {
		printRulesList(root, owner)
		return nil
	}
	text, err := rules.Load(root, name, owner)
	if err != nil {
		return err
	}
	fmt.Print(text)
	return nil
}

func printRulesList(root, owner string) {
	for _, doc := range rules.List(root, owner) {
		mark := ""
		if doc.Own {
			mark = "（このリポジトリーの rules/ の文書）"
		}
		fmt.Printf("%-12s %s%s\n", doc.Name, doc.Title, mark)
		if doc.When != "" {
			fmt.Println("             " + doc.When)
		}
	}
	fmt.Println("\nlacia rules <名前> で読めます。名前を省くと core が出ます。")
}
