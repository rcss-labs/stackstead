// Package web embeds the built single-page app served by Stackstead.
package web

import (
	"embed"
	"io/fs"
)

// dist holds the Vite build output. web/dist always contains a .gitignore,
// so the embed compiles on a tree where the UI was never built.
//
//go:embed all:dist
var dist embed.FS

// placeholder is served when the UI was not built.
//
//go:embed placeholder
var placeholder embed.FS

// Built reports whether the embedded dist holds a built app.
func Built() bool {
	_, err := fs.Stat(dist, "dist/index.html")
	return err == nil
}

// Dist returns the built SPA rooted at its index.html, or a placeholder page
// explaining that the UI was not built.
func Dist() fs.FS {
	dir := "placeholder"
	root := placeholder
	if Built() {
		dir, root = "dist", dist
	}
	sub, err := fs.Sub(root, dir)
	if err != nil {
		panic(err) // the directory is embedded at compile time; this cannot fail
	}
	return sub
}
