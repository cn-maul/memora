// Memora - 智能文档库
// 入口文件：读配置 → 装配 → 拉起生命周期
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"memora/internal/assembler"
	"memora/internal/logx"
)

func main() {
	configPath := flag.String("config", "", "配置文件路径（可选，默认由装配器探测）")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 信号处理
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	app, err := assembler.NewApp(ctx, *configPath)
	if err != nil {
		logx.Error("app", "装配失败", "err", err.Error())
		os.Exit(1)
	}

	// 启动
	if err := app.Run(); err != nil {
		logx.Error("app", "启动失败", "err", err.Error())
		os.Exit(1)
	}

	// 等待退出信号
	<-sigCh
	logx.Info("app", "收到退出信号，正在关闭")
	app.Shutdown()
}
