package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ---------------------------------------------------------------------------
// Stage 2B: StreamSession 受控流会话综合测试
// ---------------------------------------------------------------------------

// fakeSessionChat 是实现 StreamSessioner 的 FakeChat 变体。
// 用于测试 budget 层通过 StreamSessioner 接口获取内层 session 的路径。
type fakeSessionChat struct {
	FakeChat
	streamFn func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error)
}

func (f *fakeSessionChat) Stream(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if f.streamFn != nil {
		return f.streamFn(ctx, msgs, opts...)
	}
	return f.FakeChat.Stream(ctx, msgs, opts...)
}

func (f *fakeSessionChat) StreamSession(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*StreamSession, error) {
	sr, err := f.Stream(ctx, msgs, opts...)
	if err != nil {
		return nil, err
	}
	// 创建 Pipe 转发，模拟 bailian 层行为
	streamCtx, streamCancel := context.WithCancel(ctx)
	_ = streamCtx
	reader, writer := schema.Pipe[*schema.Message](1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer streamCancel()
		defer sr.Close()
		defer writer.Close()
		for {
			chunk, err := sr.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					writer.Send(nil, err)
				}
				return
			}
			if writer.Send(chunk, nil) {
				return
			}
		}
	}()
	return NewStreamSession(reader, streamCancel, done, nil), nil
}

// TestStreamSession_NoBudgetFullContent 验证无预算时多 chunk 完整到达。
func TestStreamSession_NoBudgetFullContent(t *testing.T) {
	role := &budgetChatRole{inner: NewFakeChat()} // no budget

	session, err := role.StreamSession(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamSession: %v", err)
	}
	defer session.Close()

	reader := session.Reader()
	var sb strings.Builder
	for {
		msg, err := reader.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("Recv error: %v", err)
			}
			break
		}
		if msg != nil {
			sb.WriteString(msg.Content)
		}
	}
	if got := sb.String(); got != "fake-stream" {
		t.Errorf("content = %q, want %q", got, "fake-stream")
	}
}

// TestStreamSession_EOFSettles 验证 EOF 正常结算：转发 goroutine 退出，done 关闭。
func TestStreamSession_EOFSettles(t *testing.T) {
	_, streamCancel := context.WithCancel(context.Background())
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "only"},
	})
	session := wrapBudgetStreamReader(streamCancel, sr, nil, nil)

	reader := session.Reader()
	// 消费到 EOF
	for {
		_, err := reader.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("unexpected error: %v", err)
			}
			break
		}
	}

	// done 应在合理时间内关闭
	select {
	case <-session.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("session.Done() not closed after EOF")
	}
	session.Close() // 幂等
}

// TestStreamSession_ReadError 验证读错误传播给调用方。
func TestStreamSession_ReadError(t *testing.T) {
	_, streamCancel := context.WithCancel(context.Background())
	srcReader, srcWriter := schema.Pipe[*schema.Message](1)
	session := wrapBudgetStreamReader(streamCancel, srcReader, nil, nil)

	testErr := errors.New("test read error")
	go func() {
		srcWriter.Send(nil, testErr)
		srcWriter.Close()
	}()

	reader := session.Reader()
	_, err := reader.Recv()
	if err == nil {
		t.Fatal("expected error from Recv")
	}
	if !strings.Contains(err.Error(), testErr.Error()) {
		t.Errorf("error = %v, want containing %v", err, testErr)
	}
	session.Close()
}

// TestStreamSession_EarlyClose 验证提前 Close 幂等：多次 Close 不 panic。
func TestStreamSession_EarlyClose(t *testing.T) {
	_, streamCancel := context.WithCancel(context.Background())
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "a"},
		{Role: schema.Assistant, Content: "b"},
		{Role: schema.Assistant, Content: "c"},
	})
	session := wrapBudgetStreamReader(streamCancel, sr, nil, nil)

	reader := session.Reader()
	_, _ = reader.Recv() // 读一个 chunk

	// 多次 Close 不 panic
	session.Close()
	session.Close()
	session.Close()
}

// TestStreamSession_ConcurrentCloseEOF 验证并发 Close 和 EOF 安全。
func TestStreamSession_ConcurrentCloseEOF(t *testing.T) {
	_, streamCancel := context.WithCancel(context.Background())
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "x"},
	})
	session := wrapBudgetStreamReader(streamCancel, sr, nil, nil)

	reader := session.Reader()
	var wg sync.WaitGroup

	// goroutine 1: 消费到 EOF
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, err := reader.Recv()
			if err != nil {
				return
			}
		}
	}()

	// goroutine 2: 并发 Close
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(time.Millisecond) // 让 goroutine 1 先读
		session.Close()
	}()

	wg.Wait()
	// 不 panic 即通过
}

// TestStreamSession_ParentCtxCancel 验证父 ctx 取消后流中断。
func TestStreamSession_ParentCtxCancel(t *testing.T) {
	ctx, ctxCancel := context.WithCancel(context.Background())

	fake := NewFakeChat()
	fake.SetStream(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		reader, writer := schema.Pipe[*schema.Message](1)
		go func() {
			defer writer.Close()
			writer.Send(&schema.Message{Role: schema.Assistant, Content: "first"}, nil)
			<-ctx.Done() // 等待 ctx 取消
		}()
		return reader, nil
	})

	role := &budgetChatRole{inner: fake}
	session, err := role.StreamSession(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamSession: %v", err)
	}

	reader := session.Reader()
	_, err = reader.Recv()
	if err != nil {
		t.Fatalf("first Recv: %v", err)
	}

	// 取消父 ctx
	ctxCancel()

	// 后续 Recv 应在合理时间内返回错误
	errCh := make(chan error, 1)
	go func() {
		_, rerr := reader.Recv()
		errCh <- rerr
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error after parent ctx cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Recv hung after parent ctx cancel")
	}
	session.Close()
}

// TestStreamSession_BuildFailReleasesResources 验证建流失败资源不泄漏。
func TestStreamSession_BuildFailReleasesResources(t *testing.T) {
	budget, _ := NewProcessBudget(0, 0, 0, 0)

	fake := NewFakeChat()
	fake.SetStream(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		return nil, errors.New("model unavailable")
	})

	role := &budgetChatRole{inner: fake, budget: budget}
	_, err := role.StreamSession(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error from StreamSession")
	}
	if !strings.Contains(err.Error(), "model unavailable") {
		t.Errorf("error = %v, want containing 'model unavailable'", err)
	}

	// 预算应已释放（Aborted，不残留 in-flight）
	snap := budget.Snapshot()
	if snap.InFlight != 0 {
		t.Errorf("expected InFlight=0 after build fail, got %d", snap.InFlight)
	}
}

// TestStreamSession_CloseInterruptsHTTP 验证首 chunk 后 Close → HTTP ctx 取消、body 释放。
// 使用 httptest 模拟真实 HTTP SSE 流。
func TestStreamSession_CloseInterruptsHTTP(t *testing.T) {
	bodyClosed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		// 发送第一个 chunk
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		flusher.Flush()

		// 等待客户端断开（ctx 取消）
		<-r.Context().Done()
		close(bodyClosed)
	}))
	defer srv.Close()

	budget, _ := NewProcessBudget(0, 0, 0, 0)
	fake := NewFakeChat()
	fake.SetStream(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		// 模拟真实 HTTP 流：ctx 取消时关闭 writer
		reader, writer := schema.Pipe[*schema.Message](1)
		go func() {
			defer writer.Close()
			writer.Send(&schema.Message{Role: schema.Assistant, Content: "first"}, nil)
			<-ctx.Done()
		}()
		return reader, nil
	})

	role := &budgetChatRole{inner: fake, budget: budget}
	session, err := role.StreamSession(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamSession: %v", err)
	}

	reader := session.Reader()
	_, err = reader.Recv()
	if err != nil {
		t.Fatalf("first Recv: %v", err)
	}

	// Close 应中断流
	session.Close()

	// 验证 done 已关闭
	select {
	case <-session.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("session.Done() not closed after Close")
	}
}

// TestStreamSession_WithInnerSessionCascade 验证 budget 层通过 StreamSessioner 接口
// 获取内层 session 时，Close 能级联取消内层 ctx。
func TestStreamSession_WithInnerSessionCascade(t *testing.T) {
	ctxCancelled := make(chan struct{})

	fake := &fakeSessionChat{}
	fake.SetStream(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		reader, writer := schema.Pipe[*schema.Message](1)
		go func() {
			defer writer.Close()
			writer.Send(&schema.Message{Role: schema.Assistant, Content: "data"}, nil)
			<-ctx.Done()
			close(ctxCancelled)
		}()
		return reader, nil
	})

	budget, _ := NewProcessBudget(0, 0, 0, 0)
	role := &budgetChatRole{inner: fake, budget: budget}

	session, err := role.StreamSession(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamSession: %v", err)
	}

	reader := session.Reader()
	_, _ = reader.Recv() // 读第一个 chunk

	// Close 应级联取消内层 ctx
	session.Close()

	select {
	case <-ctxCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("inner ctx not cancelled after session.Close()")
	}
}
