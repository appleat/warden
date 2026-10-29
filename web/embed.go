package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// Dist 返回构建后的前端静态资源。
func Dist() (fs.FS, error) {
	return fs.Sub(assets, "dist")
}
