package qa

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"memora/internal/contract"
)

// ── 可编程伪 LLM：精确控制流式块序列与重试结果 ──

type seqLLM struct {
	stream    []string // ChatStream 逐块输出（含 ThinkChunkPrefix 前缀的思考块）
	streamErr error    // ChatStream 建立阶段错误
	chat      string   // Chat 返回（非流式 / 空流重试）
	chatErr   error    // Chat 错误
}

func (s *seqLLM) Chat(system, user string, opts *contract.ChatOptions) (string, error) {
	return s.chat, s.chatErr
}

func (s *seqLLM) ChatStream(system, user string, opts *contract.ChatOptions, cancel <-chan struct{}) (<-chan string, error) {
	if s.streamErr != nil {
		return nil, s.streamErr
	}
	ch := make(chan string, len(s.stream))
	for _, c := range s.stream {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func (s *seqLLM) Embed(texts []string) ([][]float32, error) { return nil, nil }
func (s *seqLLM) EmbedQuery(text string) ([]float32, error) { return nil, nil }
func (s *seqLLM) Rerank(query string, docs []string) ([]float64, error) {
	return nil, errors.New("not configured")
}

// recordingStorage 在伪存储基础上记录会话删除，验证取消/失败回滚。
type recordingStorage struct {
	*fakeStorage
	deletes []int64
}

func (r *recordingStorage) QASessionsDelete(id int64) error {
	r.deletes = append(r.deletes, id)
	return nil
}

// Ask 与 Execute(sink=nil) 输出完全一致。
func TestExecuteNilSinkEqualsAsk(t *testing.T) {
	st := newFileStorage()
	ev := &fakeEvents{}
	m := newFakeModule(st, nil, ev)
	req := &contract.QARequest{Question: "测试问题", Mode: "file", FileID: 1}

	askResp, askErr := m.Ask(req)
	gotResp, gotErr := m.Execute(context.Background(), req, nil)

	if askErr != nil || gotErr != nil {
		t.Fatalf("均不应报错: Ask=%v Execute=%v", askErr, gotErr)
	}
	if !reflect.DeepEqual(askResp, gotResp) {
		t.Fatalf("Ask 与 Execute(sink=nil) 不一致:\nAsk     =%+v\nExecute =%+v", askResp, gotResp)
	}
}

// Execute 流式 sink 与 AskStream 对同一伪 LLM 产生完全一致的块序列与最终结果。
func TestExecuteStreamMatchesAskStream(t *testing.T) {
	fileReq := &contract.QARequest{Question: "测试问题", Mode: "file", FileID: 1}
	scenarios := []struct {
		name string
		llm  ILLM
		req  *contract.QARequest
	}{
		{"content", &seqLLM{stream: []string{"你", "好"}}, fileReq},
		{"thinking", &seqLLM{stream: []string{contract.ThinkChunkPrefix + "先分析", "答案"}}, fileReq},
		{"error-chunk", &seqLLM{stream: []string{"部分", "__ERROR__:上游超时"}}, fileReq},
		{"stream-err", &seqLLM{streamErr: errors.New("连接失败")}, fileReq},
		{"empty-retry", &seqLLM{stream: []string{""}, chat: "重试回答"}, fileReq},
		{"empty-retry-empty-chat", &seqLLM{stream: []string{""}, chat: ""}, fileReq},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			// AskStream 侧
			streamM := newFakeModule(newFileStorage(), sc.llm, &fakeEvents{})
			ch, done := streamM.AskStream(sc.req, make(chan struct{}))
			var streamChunks []string
			for c := range ch {
				streamChunks = append(streamChunks, c)
			}
			streamRes := <-done

			// Execute 流式 sink 侧
			execM := newFakeModule(newFileStorage(), sc.llm, &fakeEvents{})
			ctx, cfunc := context.WithCancel(context.Background())
			defer cfunc()
			var execChunks []string
			sink := &QASink{OnChunk: func(c string) { execChunks = append(execChunks, c) }}
			execResp, execErr := execM.Execute(ctx, sc.req, sink)

			if !reflect.DeepEqual(streamChunks, execChunks) {
				t.Fatalf("块序列不一致:\nAskStream=%v\nExecute =%v", streamChunks, execChunks)
			}
			if streamRes.Error != "" || execErr != nil {
				wantErr := streamRes.Error
				var gotErr string
				if execErr != nil {
					gotErr = execErr.Error()
				}
				if wantErr != gotErr {
					t.Fatalf("错误不一致: AskStream=%q Execute=%q", wantErr, gotErr)
				}
				return
			}
			if execResp == nil {
				t.Fatalf("Execute 应返回响应")
			}
			if execResp.Answer != streamRes.Answer {
				t.Fatalf("最终 answer 不一致: AskStream=%q Execute=%q", streamRes.Answer, execResp.Answer)
			}
			if !reflect.DeepEqual(execResp.Sources, streamRes.Sources) {
				t.Fatalf("Sources 不一致: AskStream=%v Execute=%v", streamRes.Sources, execResp.Sources)
			}
			if execResp.SessionID != streamRes.SessionID {
				t.Fatalf("SessionID 不一致: AskStream=%d Execute=%d", streamRes.SessionID, execResp.SessionID)
			}
		})
	}
}

// Execute 流式空上下文：把"未找到"文本推给 sink，并正常保存 + 广播 qa_ready。
func TestExecuteStreamEmptyContextEmitsNotFound(t *testing.T) {
	m := newFakeModule(&fakeStorage{}, nil, &fakeEvents{})

	ctx, cfunc := context.WithCancel(context.Background())
	defer cfunc()
	var got []string
	resp, err := m.Execute(ctx, &contract.QARequest{Question: "测试问题", Mode: "global"}, &QASink{
		OnChunk: func(c string) { got = append(got, c) },
	})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	want := "根据现有文档，未找到相关信息。"
	if resp.Answer != want {
		t.Fatalf("answer=%q", resp.Answer)
	}
	if !reflect.DeepEqual(got, []string{want}) {
		t.Fatalf("未找到文本未推给 sink: %v", got)
	}
	if resp.SessionID != 1 {
		t.Fatalf("sessionId=%d", resp.SessionID)
	}
}

// Execute 流式取消：ctx 预先取消 → 返回 errCanceled，删除新建会话、
// 不调 SaveExchange、不广播 qa_ready、不输出任何块。
func TestExecuteStreamCancelRollsBack(t *testing.T) {
	st := &recordingStorage{fakeStorage: newFileStorage()}
	ev := &fakeEvents{}
	m := New(st, &seqLLM{stream: []string{"你", "好"}}, ev, 30000)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预先取消，确定性触发取消分支

	var got []string
	resp, err := m.Execute(ctx, &contract.QARequest{Question: "测试问题", Mode: "file", FileID: 1}, &QASink{
		OnChunk:  func(c string) { got = append(got, c) },
		Canceled: func() bool { return false },
	})
	if err == nil || err.Error() != "已取消" {
		t.Fatalf("应返回 errCanceled，got %v", err)
	}
	if resp != nil {
		t.Fatalf("取消时不应有响应: %+v", resp)
	}
	if len(st.deletes) != 1 {
		t.Fatalf("取消应删除新建会话，实际删除 %d 次: %v", len(st.deletes), st.deletes)
	}
	if len(st.calls()) != 0 {
		t.Fatalf("取消后不应调用 SaveExchange，实际 %d", len(st.calls()))
	}
	if ev.count() != 0 {
		t.Fatalf("取消路径不应广播 qa_ready，实际 %d", ev.count())
	}
	if len(got) != 0 {
		t.Fatalf("取消时应停止输出，实际收到 %v", got)
	}
}

// AskStream 包装层取消：cancel 预先关闭 → done 携带"已取消"，新建会话被删除。
func TestAskStreamCancelDeliversCanceled(t *testing.T) {
	st := &recordingStorage{fakeStorage: newFileStorage()}
	ev := &fakeEvents{}
	m := New(st, &seqLLM{stream: []string{"你", "好"}}, ev, 30000)

	cancel := make(chan struct{})
	close(cancel) // 预先取消
	ch, done := m.AskStream(&contract.QARequest{Question: "测试问题", Mode: "file", FileID: 1}, cancel)
	for range ch {
	}
	res := <-done
	if res.Error != "已取消" {
		t.Fatalf("done.Error=%q, want 已取消", res.Error)
	}
	if len(st.deletes) != 1 {
		t.Fatalf("取消应删除新建会话，实际删除 %d 次: %v", len(st.deletes), st.deletes)
	}
	if len(st.calls()) != 0 {
		t.Fatalf("取消后不应调用 SaveExchange，实际 %d", len(st.calls()))
	}
	if ev.count() != 0 {
		t.Fatalf("取消路径不应广播 qa_ready，实际 %d", ev.count())
	}
}
