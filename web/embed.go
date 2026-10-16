package web

import "embed"

// DistFS embeds the compiled WebUI single-page application into the binary.
//
//go:embed dist/*
var DistFS embed.FS
