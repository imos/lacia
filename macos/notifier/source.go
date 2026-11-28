// Package notifier carries the source of the macOS notifier app inside the
// lacia binary, so that lacia setup --notifier builds the app of the same
// version as the binary without fetching anything.
package notifier

import "embed"

//go:embed LaciaNotifier.swift manage.py brand.png render-icon.swift
var Source embed.FS
