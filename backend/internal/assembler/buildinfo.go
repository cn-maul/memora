// Package assembler 装配根：按顺序 new 并接线
// buildinfo.go：构建期注入的版本信息。
// 由 go build -ldflags "-X memora/internal/assembler.BuildVersion=..." 在发布时注入。
// 默认值保证本地 go run / go build 不经 ldflags 也能正常运行。
package assembler

// 构建期注入（go build -ldflags "-X ..."）。默认 "dev" 保证本地构建可运行。
var (
	BuildVersion = "dev"
	BuildCommit  = ""
	BuildTime    = ""
)
