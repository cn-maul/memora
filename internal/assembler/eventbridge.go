// eventbridge.go — 将内部事件总线（events.Module）桥接到 Wails 全局 EventBus。
// 后端业务模块通过 a.Events.Notify("index_progress", data) 推送内部事件，
// 本桥接订阅所有 topic 并转发到 Wails 事件（命名空间 "memora:"），前端通过 Events.On("memora:index_progress") 接收。
package assembler

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

// WailsEventBridge 把内部 events.Module 广播转发到 Wails 全局 Event.Emit。
// 注意：因 events.Subscribe 通过函数指针比较实现取消订阅，
// 本结构体使用具名方法作为回调，确保可正确移除。
type WailsEventBridge struct {
	app *App
}

var bridgeTopics = []string{
	"index_progress", "extract_failed", "commit_done",
	"tag_done", "suggestion_new", "files_changed",
	"task_queue", "settings_changed", "stats_updated", "qa_ready",
}

// NewWailsEventBridge 创建桥接实例，订阅所有已知 topic。
func NewWailsEventBridge(a *App) *WailsEventBridge {
	b := &WailsEventBridge{app: a}
	for _, t := range bridgeTopics {
		topic := t
		a.Events.Subscribe(topic, func(data interface{}) {
			b.forward(topic, data)
		})
	}
	return b
}

// forward 将内部事件转发到 Wails 全局事件。
// 若 application.Get() 尚未就绪（Run 之前），静默跳过。
func (b *WailsEventBridge) forward(topic string, data interface{}) {
	wailsApp := application.Get()
	if wailsApp == nil {
		return
	}
	wailsApp.Event.Emit("memora:"+topic, data)
}