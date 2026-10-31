package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
)

// LocalModelServer 是本机 OpenAI 兼容模型对端（chat / embeddings / rerank）。
//
// 它只替身 HTTP 对端：预算预留与承诺、角色 adapter、guardedEmbedder、rerank 校验链
// 都是生产代码。服务端计数器用于离线证明"每次尝试都真的发了一次请求"。
type LocalModelServer struct {
	Server *httptest.Server

	chatHits   atomic.Int64
	embedHits  atomic.Int64
	rerankHits atomic.Int64

	mu       sync.Mutex
	behavior ModelBehavior

	chatAborts  atomic.Int64
	embedAborts atomic.Int64
}

// ModelBehavior 控制本机模型对端的响应行为（仅测试/离线证据使用）。
type ModelBehavior struct {
	// ChatUsage 控制 chat 响应是否携带 usage。
	ChatUsage bool
	// ChatStatus 非 0 且非 200 时直接返回该状态码（模拟非预期 HTTP 状态）。
	ChatStatus int
	// ChatErrorBody 非空时与 ChatStatus 一起写出。
	// 用于验证证据脱敏：服务端正文不得进入 Report.StopReason。
	ChatErrorBody string
	// ChatRedirect 非空时返回 307 指向该地址。
	// 用于验证"重定向被禁止后不得重试"：本机目标端可计数应当为 0。
	ChatRedirect string
	// ChatAbortFirst 让前 N 次 chat 请求以连接中断结束（模拟传输层错误）。
	ChatAbortFirst int
	// ChatGate 让 chat 请求阻塞到 release 或调用方 ctx 结束。
	ChatGate *Gate

	EmbedStatus     int
	EmbedAbortFirst int
	// EmbedDims 非 0 时按该维度返回向量（用于验证响应维度校验确实生效）。
	EmbedDims int
	EmbedGate *Gate

	RerankStatus int
}

// DefaultModelBehavior 返回默认行为：chat 带 usage、全部 200。
func DefaultModelBehavior() ModelBehavior {
	return ModelBehavior{ChatUsage: true}
}

// NewLocalModelServer 启动本机模型对端。
func NewLocalModelServer() *LocalModelServer {
	srv := &LocalModelServer{behavior: DefaultModelBehavior()}
	srv.Server = httptest.NewServer(http.HandlerFunc(srv.handle))
	return srv
}

// SetBehavior 覆盖响应行为（计数器保持不变）。
func (s *LocalModelServer) SetBehavior(b ModelBehavior) {
	s.mu.Lock()
	s.behavior = b
	s.mu.Unlock()
	s.chatAborts.Store(int64(b.ChatAbortFirst))
	s.embedAborts.Store(int64(b.EmbedAbortFirst))
}

func (s *LocalModelServer) currentBehavior() ModelBehavior {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.behavior
}

func (s *LocalModelServer) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/chat/completions":
		s.handleChat(w, r)
	case "/embeddings":
		s.handleEmbed(w, r)
	case "/rerank":
		s.handleRerank(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *LocalModelServer) handleChat(w http.ResponseWriter, r *http.Request) {
	s.chatHits.Add(1)
	b := s.currentBehavior()
	if b.ChatGate != nil {
		if err := b.ChatGate.wait(r.Context()); err != nil {
			return
		}
	}
	if s.chatAborts.Add(-1) >= 0 {
		// ErrAbortHandler 让 net/http 直接断开连接（不写响应），
		// 客户端看到传输层错误而不是 HTTP 状态码。
		panic(http.ErrAbortHandler)
	}
	if b.ChatRedirect != "" {
		http.Redirect(w, r, b.ChatRedirect, http.StatusTemporaryRedirect)
		return
	}
	if b.ChatStatus != 0 && b.ChatStatus != http.StatusOK {
		w.WriteHeader(b.ChatStatus)
		if b.ChatErrorBody != "" {
			_, _ = io.WriteString(w, b.ChatErrorBody)
		}
		return
	}
	body := map[string]any{
		"id":      "probe-chat",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   "probe-chat",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": "probe-ok"},
			"finish_reason": "stop",
		}},
	}
	if b.ChatUsage {
		body["usage"] = map[string]any{"prompt_tokens": 3, "completion_tokens": 5, "total_tokens": 8}
	}
	writeJSON(w, body)
}

func (s *LocalModelServer) handleEmbed(w http.ResponseWriter, r *http.Request) {
	s.embedHits.Add(1)
	b := s.currentBehavior()
	if b.EmbedGate != nil {
		if err := b.EmbedGate.wait(r.Context()); err != nil {
			return
		}
	}
	if s.embedAborts.Add(-1) >= 0 {
		panic(http.ErrAbortHandler)
	}
	if b.EmbedStatus != 0 && b.EmbedStatus != http.StatusOK {
		w.WriteHeader(b.EmbedStatus)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		Input json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(raw, &req)
	inputs := parseEmbedInputs(req.Input)
	dims := b.EmbedDims
	if dims <= 0 {
		dims = 1
	}
	data := make([]any, 0, len(inputs))
	for i := range inputs {
		vec := make([]float64, dims)
		for j := range vec {
			vec[j] = float64(i+j+1) * 0.001
		}
		data = append(data, map[string]any{"object": "embedding", "embedding": vec, "index": i})
	}
	writeJSON(w, map[string]any{
		"object": "list",
		"data":   data,
		"model":  "probe-embed",
		"usage":  map[string]any{"prompt_tokens": len(inputs), "completion_tokens": 0, "total_tokens": len(inputs)},
	})
}

func (s *LocalModelServer) handleRerank(w http.ResponseWriter, r *http.Request) {
	s.rerankHits.Add(1)
	b := s.currentBehavior()
	if b.RerankStatus != 0 && b.RerankStatus != http.StatusOK {
		w.WriteHeader(b.RerankStatus)
		return
	}
	writeJSON(w, map[string]any{
		"id": "probe-rerank",
		"results": []any{
			map[string]any{"index": 0, "relevance_score": 0.9},
		},
	})
}

// parseEmbedInputs 解析 OpenAI 兼容的 input 字段（数组或单字符串）。
func parseEmbedInputs(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// URL 返回 Chat/Embedding 的 base URL（Eino 客户端在其后追加 /chat/completions 或 /embeddings）。
func (s *LocalModelServer) URL() string {
	if s == nil || s.Server == nil {
		return ""
	}
	return s.Server.URL
}

// RerankURL 返回 Rerank 的完整 URL（BuildRerankRole 直接使用该值）。
func (s *LocalModelServer) RerankURL() string {
	if s == nil || s.Server == nil {
		return ""
	}
	return s.Server.URL + "/rerank"
}

// ChatHits/EmbedHits/RerankHits 返回各端点的服务端实际接收请求数。
func (s *LocalModelServer) ChatHits() int64   { return s.chatHits.Load() }
func (s *LocalModelServer) EmbedHits() int64  { return s.embedHits.Load() }
func (s *LocalModelServer) RerankHits() int64 { return s.rerankHits.Load() }

// Hits 按步骤类型返回服务端命中数。
func (s *LocalModelServer) Hits(kind StepKind) int64 {
	switch kind {
	case StepChat:
		return s.ChatHits()
	case StepEmbed:
		return s.EmbedHits()
	case StepRerank:
		return s.RerankHits()
	default:
		return 0
	}
}

// Close 关闭本机模型对端。
func (s *LocalModelServer) Close() {
	if s == nil || s.Server == nil {
		return
	}
	s.Server.Close()
}

// Gate 让服务端 handler 阻塞到显式放行或调用方 ctx 结束。
// 用于并发上限与 deadline 在"探针入口"生效的确定性验证。
type Gate struct {
	reached     chan struct{}
	release     chan struct{}
	reachedOnce sync.Once
	releaseOnce sync.Once
}

// NewGate 创建阻塞闸门。
func NewGate() *Gate {
	return &Gate{reached: make(chan struct{}), release: make(chan struct{})}
}

// Reached 在首次有请求到达时关闭（调用方可据此等待"请求已发出"）。
func (g *Gate) Reached() <-chan struct{} { return g.reached }

// Release 放行阻塞中的请求。
func (g *Gate) Release() { g.releaseOnce.Do(func() { close(g.release) }) }

func (g *Gate) wait(ctx context.Context) error {
	g.reachedOnce.Do(func() { close(g.reached) })
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// String 便于在证据日志中描述闸门状态（不参与行为）。
func (g *Gate) String() string {
	if g == nil {
		return "gate<nil>"
	}
	return fmt.Sprintf("gate{%p}", g)
}
