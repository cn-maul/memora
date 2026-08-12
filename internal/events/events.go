// Package events 极简发布/订阅模块
// 实现要求：内部即 topic → 一组函数的简单 map，无队列、无持久化
package events

import (
	"fmt"
	"sync"

	"memora/internal/logx"
)

// Handler 事件处理函数
type Handler func(data interface{})

// Module 事件模块
type Module struct {
	mu       sync.RWMutex
	handlers map[string][]Handler
}

// New 创建事件模块
func New() *Module {
	return &Module{
		handlers: make(map[string][]Handler),
	}
}

// Notify 同步广播主题事件
// 某订阅者出错不影响其他，记日志
func (m *Module) Notify(topic string, data interface{}) {
	m.mu.RLock()
	handlers, ok := m.handlers[topic]
	m.mu.RUnlock()

	if !ok {
		return
	}

	for _, h := range handlers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logx.Error("events", "订阅者处理崩溃", "topic", topic, "panic", fmt.Sprintf("%v", r))
				}
			}()
			h(data)
		}()
	}
}

// Subscribe 订阅主题，返回取消订阅函数
func (m *Module) Subscribe(topic string, handler Handler) func() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.handlers[topic] = append(m.handlers[topic], handler)

	// 返回取消订阅函数
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		handlers := m.handlers[topic]
		for i, h := range handlers {
			// 通过函数指针比较
			if fmt.Sprintf("%p", h) == fmt.Sprintf("%p", handler) {
				m.handlers[topic] = append(handlers[:i], handlers[i+1:]...)
				break
			}
		}
	}
}
