package lacia

import (
	"embed"
	"io/fs"
	"strings"
)

//go:embed docs/design/*.md README.md AGENTS.md LICENSE
var referenceFiles embed.FS

const DesignRoot = "/design/"

// DesignDocument reads an embedded document by its public name. The index
// uses the empty name. Other names do not contain paths or extensions.
func DesignDocument(name string) (string, []byte, error) {
	source := designSource(name)
	if source == "" {
		return "", nil, fs.ErrNotExist
	}
	content, err := referenceFiles.ReadFile(source)
	return source, content, err
}

func designSource(name string) string {
	switch name {
	case "":
		return "docs/design/README.md"
	case "setup":
		return "README.md"
	case "development":
		return "AGENTS.md"
	case "license":
		return "LICENSE"
	default:
		if name == "README" || strings.ContainsAny(name, "/\\.") {
			return ""
		}
		return "docs/design/" + name + ".md"
	}
}

// DesignDocumentExists checks a document name without reading its contents.
func DesignDocumentExists(name string) bool {
	_, err := fs.Stat(referenceFiles, designSource(name))
	return err == nil
}

// DesignURL maps a source path to the site URL of the embedded document.
func DesignURL(source string) (string, bool) {
	switch source {
	case "docs/design/README.md":
		return DesignRoot, true
	case "README.md":
		return DesignRoot + "setup", true
	case "AGENTS.md":
		return DesignRoot + "development", true
	case "LICENSE":
		return DesignRoot + "license", true
	}
	name, ok := strings.CutPrefix(source, "docs/design/")
	if !ok {
		return "", false
	}
	name, ok = strings.CutSuffix(name, ".md")
	if !ok {
		return "", false
	}
	if source != designSource(name) || !DesignDocumentExists(name) {
		return "", false
	}
	return DesignRoot + name, true
}
