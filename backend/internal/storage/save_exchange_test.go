package storage

import "testing"

// TestSaveExchange_NewSession 新建会话成功时返回 sessionID 与消息总数，
// 按会话可查到 2 条消息且顺序为 user → assistant。
func TestSaveExchange_NewSession(t *testing.T) {
	m, err := New(t.TempDir(), 4)
	if err != nil {
		t.Fatalf("新建存储模块失败: %v", err)
	}
	defer m.Close()

	sessionID, count, err := m.SaveExchange(0, "global", 0, "你好", "你好！有什么可以帮你？", `[{"relPath":"a.md"}]`, 1000)
	if err != nil {
		t.Fatalf("SaveExchange 失败: %v", err)
	}
	if sessionID == 0 {
		t.Fatal("期望返回非零 sessionID")
	}
	if count != 2 {
		t.Fatalf("期望消息总数 2, 实际 %d", count)
	}

	msgs, err := m.QAMessagesBySession(sessionID)
	if err != nil {
		t.Fatalf("查询会话消息失败: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("期望 2 条消息, 实际 %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "你好" {
		t.Fatalf("第一条消息应为 user: %v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "你好！有什么可以帮你？" {
		t.Fatalf("第二条消息应为 assistant: %v", msgs[1])
	}
	if msgs[1].Sources != `[{"relPath":"a.md"}]` {
		t.Fatalf("assistant 消息 sources 不符: %s", msgs[1].Sources)
	}
}

// TestSaveExchange_ReuseSession 复用已有会话追加回合，消息按 id 顺序累积。
func TestSaveExchange_ReuseSession(t *testing.T) {
	m, err := New(t.TempDir(), 4)
	if err != nil {
		t.Fatalf("新建存储模块失败: %v", err)
	}
	defer m.Close()

	sid, _, err := m.SaveExchange(0, "file", 42, "q1", "a1", "", 1000)
	if err != nil {
		t.Fatalf("第一回合失败: %v", err)
	}

	// 复用同一会话追加第二回合
	_, count, err := m.SaveExchange(sid, "file", 42, "q2", "a2", "", 2000)
	if err != nil {
		t.Fatalf("复用会话失败: %v", err)
	}
	if count != 2 {
		t.Fatalf("期望每回合 2 条消息, 实际 %d", count)
	}

	msgs, err := m.QAMessagesBySession(sid)
	if err != nil {
		t.Fatalf("查询会话消息失败: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("期望 4 条消息, 实际 %d", len(msgs))
	}
	if msgs[2].Role != "user" || msgs[2].Content != "q2" {
		t.Fatalf("第三条第消息应为 user q2: %v", msgs[2])
	}
	if msgs[3].Role != "assistant" || msgs[3].Content != "a2" {
		t.Fatalf("第四条消息应为 assistant a2: %v", msgs[3])
	}
}

// TestSaveExchange_RollbackOnFailure 注入失败（非零但不存在 的 sessionID 校验失败）
// 时整体回滚，无任何会话或消息残留。
func TestSaveExchange_RollbackOnFailure(t *testing.T) {
	m, err := New(t.TempDir(), 4)
	if err != nil {
		t.Fatalf("新建存储模块失败: %v", err)
	}
	defer m.Close()

	sid, count, err := m.SaveExchange(12345, "global", 0, "你好", "回答", "", 1000)
	if err == nil {
		t.Fatal("期望不存在的会话返回错误")
	}
	if sid != 0 || count != 0 {
		t.Fatalf("失败时返回应清零: sid=%d count=%d", sid, count)
	}

	// 无任何残留：没有新建会话，也没有任何消息
	sessions, err := m.QASessionsList()
	if err != nil {
		t.Fatalf("列出会话失败: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("失败后不应残留会话, 实际 %d", len(sessions))
	}
	var msgs int
	if err := m.db.QueryRow("SELECT COUNT(*) FROM qa_messages").Scan(&msgs); err != nil {
		t.Fatalf("统计消息失败: %v", err)
	}
	if msgs != 0 {
		t.Fatalf("失败后不应残留消息, 实际 %d", msgs)
	}
}
