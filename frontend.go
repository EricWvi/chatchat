// Package chatchat embeds the built frontend into the Go binary.
package chatchat

import (
	"embed"
	"io/fs"
)

// dist is produced by `npm run build` in frontend/. The tracked .gitkeep
// keeps the directory present so the package compiles before a build.
//
//go:embed all:frontend/dist
var dist embed.FS

// Frontend returns the built frontend rooted at its dist directory.
func Frontend() fs.FS {
	sub, err := fs.Sub(dist, "frontend/dist")
	if err != nil {
		panic(err) // the path is a compile-time constant
	}
	return sub
}
