// Package web embeds the built widget bundle served at /widget.js.
//
// static/widget.js is produced by the widget build (npm --prefix widget run
// build); rebuild it before `go build` to ship widget changes.
package web

import _ "embed"

// WidgetJS is the widget bundle.
//
//go:embed static/widget.js
var WidgetJS []byte
