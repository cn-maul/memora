// Memora - 智能文档库 (Wails v3 入口)
// 初始化 Wails 应用 → 装配内部模块 → 注册服务 → 启动原生窗口
package main

import (
	"context"
	"embed"
	"log"

	"memora/internal/assembler"
	"memora/internal/logx"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

// main 应用入口。Wails v3 在 dev 模式下通过 Vite dev server 自动同步前端
// 资源与 bindings；生产构建时，frontend/dist 通过 go:embed 内嵌到 exe。
func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 装配内部模块（去掉了 Transport；HTTP 与原生窗口由 Wails 接管）
	app, err := assembler.NewApp(ctx, "")
	if err != nil {
		logx.Error("app", "装配失败", "err", err.Error())
		log.Fatal(err)
	}

	// 启动内部模块（文件监视、任务队列、reconcile 后台循环）
	if err := app.RunNative(); err != nil {
		logx.Error("app", "启动失败", "err", err.Error())
		log.Fatal(err)
	}

	// 注册服务 —— 将 13 个服务一次性注册为 Wails Service，
	// wails3 generate bindings 会根据这些 service 的导出方法自动生成 TS 类型。
	wailsApp := application.New(application.Options{
		Name:        "Memora",
		Description: "智能文档库",
		Services: []application.Service{
			application.NewService(&assembler.WorkspaceService{App: app}),
			application.NewService(&assembler.FilesService{App: app}),
			application.NewService(&assembler.SearchService{App: app}),
			application.NewService(&assembler.IndexService{App: app}),
			application.NewService(&assembler.BrowseService{App: app}),
			application.NewService(&assembler.TagsService{App: app}),
			application.NewService(&assembler.QAService{App: app}),
			application.NewService(&assembler.StatsService{App: app}),
			application.NewService(&assembler.CommitsService{App: app}),
			application.NewService(&assembler.SettingsService{App: app}),
			application.NewService(&assembler.TestService{App: app}),
			application.NewService(&assembler.QueueService{App: app}),
			application.NewService(&assembler.DiagnosticsService{App: app}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		// 启动完成后关闭应用（macOS 上默认关闭最后窗口退出；Windows/Linux 用 OnShutdown）
		OnShutdown: func() {
			app.Shutdown()
		},
	})

	// 创建主窗口（生产模式 URL="/" 走内嵌资产；dev 模式由 Wails dev server 接管）
	wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "Memora",
		Width: 1280,
		Height: 800,
		URL:   "/",
	})

	// 启动 Wails 应用（阻塞，直到应用退出）
	if err := wailsApp.Run(); err != nil {
		log.Fatal(err)
	}
}