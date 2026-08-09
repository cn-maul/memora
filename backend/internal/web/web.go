// Package web 提供内嵌的前端静态资源（构建时由 build.bat 将 frontend/dist 复制到本包 dist/）
// 使 memora.exe 成为自包含产物，不再依赖可执行文件旁的 web/ 目录。
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS 返回前端静态资源文件系统，根即为 dist 内容（index.html / assets / ...）。
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	return sub
}

// HasIndex 报告嵌入内容是否包含可用的前端入口（index.html）。
// dist 未构建（仅含占位文件）时返回 false，调用方据此退化为仅 API 服务。
func HasIndex() bool {
	_, err := fs.Stat(dist, "dist/index.html")
	return err == nil
}
