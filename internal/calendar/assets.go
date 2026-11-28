package calendar

import (
	_ "embed"
	"html/template"
)

// TemplateText defines the template "calendar", which takes a View.
//
//go:embed calendar.tmpl
var TemplateText string

// CSS styles the markup of the "calendar" template.
//
//go:embed calendar.css
var CSS string

// JS moves the current-time line once a minute. Nothing else needs script.
//
//go:embed calendar.js
var JS string

func AddTemplate(t *template.Template) (*template.Template, error) {
	return t.Parse(TemplateText)
}
