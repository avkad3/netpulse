// Package web embeds the dashboard so NetPulse ships as a single binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static returns the dashboard files rooted at static/.
func Static() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err) // the embed directive guarantees the directory exists
	}
	return sub
}
