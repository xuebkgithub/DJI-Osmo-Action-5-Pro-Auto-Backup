// Package web 嵌入构建后的前端静态资源。V0.5 之前只有占位页面。
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var dist embed.FS

// Dist 返回以 dist 为根的文件系统。
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
