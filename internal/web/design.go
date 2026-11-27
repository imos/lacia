package web

import (
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/imos/lacia"
	"github.com/yuin/goldmark/parser"
)

func (s *Server) designDocument(w http.ResponseWriter, r *http.Request) {
	source, content, err := lacia.DesignDocument(r.PathValue("name"))
	if err != nil {
		s.fail(w, http.StatusNotFound, "設計文書が見つかりませんでした。")
		return
	}
	body := string(content)
	title, _, _ := strings.Cut(body, "\n")
	data := fileData{Path: source, Markdown: strings.HasSuffix(source, ".md"), URL: r.URL.Path}
	data.Source = data.Markdown && r.URL.Query().Get("view") == "source"
	if data.Markdown && !data.Source {
		data.Body = renderMarkdownLinks(body, func(dest string) string {
			return designURL(source, dest)
		}, parser.WithContext(parser.NewContext(parser.WithIDs(designHeadingIDs{}))))
	} else {
		data.Plain = body
	}
	s.render(w, http.StatusOK, "file", strings.TrimPrefix(title, "# "), data)
}

func designURL(source, dest string) string {
	u, err := url.Parse(dest)
	if err != nil || u.IsAbs() || u.Host != "" || u.Path == "" || strings.HasPrefix(u.Path, "/") {
		return dest
	}
	target, ok := lacia.DesignURL(path.Join(path.Dir(source), u.Path))
	if !ok {
		return dest
	}
	u.Path, u.RawPath = target, ""
	return u.String()
}
