// Package webui embeds the built frontend (frontend/dist, copied here by
// `make web`). When the UI has not been built the directory only contains a
// placeholder and the server shows an explanatory page.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the UI file system rooted at the build output.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
