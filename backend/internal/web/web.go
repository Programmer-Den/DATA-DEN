// Package web holds the built Angular website, embedded into the binary.
//
// `npm run build` in frontend/ writes the website into files/build/. Without
// that build the server explains how to build it instead of showing the site.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:files
var files embed.FS

// Files returns the built website, with index.html at its root.
func Files() fs.FS {
	site, err := fs.Sub(files, "files/build")
	if err != nil {
		// fs.Sub only fails for an invalid path, and this one is a constant.
		panic(err)
	}
	return site
}
