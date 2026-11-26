package task

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnerAndReviewers(t *testing.T) {
	defer func(name string, r []string) { OwnerName, reviewers = name, r }(OwnerName, reviewers)
	check := namesReviewer
	// A repository that says nothing keeps the default name.
	t.Setenv("LACIA_USER", "")
	root := t.TempDir()
	SetOwner(root)
	if OwnerName != "オーナーさん" || !check("オーナーさんに見ていただきます。") || check("田中さんに見ていただきます。") {
		t.Errorf("defaults changed: %q", OwnerName)
	}
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.WriteFile(filepath.Join(root, ".lacia", "config.json"), []byte(`{"user":"田中さん","reviewers":["佐藤さん"]}`), 0o644)
	SetOwner(root)
	if OwnerName != "田中さん" || !check("田中さんに見ていただきます。") || !check("佐藤さんに伺います。") || check("オーナーさんに見ていただきます。") {
		t.Errorf("the repository's names were not used: %q %v", OwnerName, reviewers)
	}
	// The environment, set by the server for its conversations, wins.
	t.Setenv("LACIA_USER", "鈴木さん")
	SetOwner(root)
	if OwnerName != "鈴木さん" || !check("鈴木さんに見ていただきます。") {
		t.Errorf("LACIA_USER was not used: %q", OwnerName)
	}
	// A second root may have neither a name nor the first root's reviewers.
	t.Setenv("LACIA_USER", "")
	SetOwner(t.TempDir())
	if OwnerName != DefaultOwner || check("佐藤さんに伺います。") {
		t.Errorf("the second repository inherited a name: %q %v", OwnerName, reviewers)
	}
}
