package providers

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// 本文件提供全角色离线 fake 实现（stage2-plan §3.6/§7.5）。
//
// 关键约束：fake 实现与生产适配器实现【相同的角色接口】（ChatRole/EmbedRole/RerankRole），
// 并通过 ProviderRegistry 接口注入，装配路径与生产一致；不引入独立于生产代码的
// 测试专用模型构造逻辑。业务方拿到的是 ChatRole/EmbedRole/RerankRole，
// 无法也无需区分 fake 与生产实现。

// FakeChat 是可配置的 ChatRole fake。
type FakeChat struct {
	mu sync.Mutex

	generateFn func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error)
	streamFn   func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error)

	generateCalls int
	streamCalls   int
	lastInput     []*schema.Message
}

// 编译期断言：fake 满足与生产一致的角色接口。
var (
	_ ChatRole   = (*FakeChat)(nil)
	_ EmbedRole  = (*FakeEmbed)(nil)
	_ RerankRole = (*FakeRerank)(nil)
)

// NewFakeChat 创建默认成功的 ChatRole fake。
func NewFakeChat() *FakeChat { return &FakeChat{} }

// SetGenerate 覆盖 Generate 行为（返回内容/错误），用于确定性测试。
func (f *FakeChat) SetGenerate(fn func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generateFn = fn
}

// SetStream 覆盖 Stream 行为。
func (f *FakeChat) SetStream(fn func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamFn = fn
}

// Generate 实现 ChatRole。默认返回固定 assistant 消息。
func (f *FakeChat) Generate(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	f.mu.Lock()
	f.generateCalls++
	f.lastInput = msgs
	fn := f.generateFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, msgs, opts...)
	}
	return &schema.Message{Role: schema.Assistant, Content: "fake-chat-reply"}, nil
}

// Stream 实现 ChatRole。默认返回由数组构造的只读流（两段拼接）。
func (f *FakeChat) Stream(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	f.mu.Lock()
	f.streamCalls++
	f.lastInput = msgs
	fn := f.streamFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, msgs, opts...)
	}
	return schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "fake-"},
		{Role: schema.Assistant, Content: "stream"},
	}), nil
}

// GenerateCalls 返回 Generate 调用次数。
func (f *FakeChat) GenerateCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.generateCalls
}

// StreamCalls 返回 Stream 调用次数。
func (f *FakeChat) StreamCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streamCalls
}

// LastInput 返回最近一次调用的输入消息（只读用途）。
func (f *FakeChat) LastInput() []*schema.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastInput
}

// Profile 返回固定 Profile（满足测试中的 Profile() 接口断言）。
func (f *FakeChat) Profile() ProviderProfile {
	return ProviderProfile{ProviderName: "fake", Protocol: "test", Model: "fake-model", Endpoint: "http://fake"}
}

// Capabilities 返回固定能力集。
func (f *FakeChat) Capabilities() Capabilities {
	return Capabilities{Stream: true, Usage: true}
}

// FakeEmbed 是可配置的 EmbedRole fake，输出确定维度、有限数值的向量。
type FakeEmbed struct {
	mu sync.Mutex

	dims    int
	modelID string
	embedFn func(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error)
	calls   int
}

// NewFakeEmbed 创建返回 dims 维确定向量的 EmbedRole fake。
func NewFakeEmbed(dims int, modelID string) *FakeEmbed {
	return &FakeEmbed{dims: dims, modelID: modelID}
}

// SetEmbed 覆盖 EmbedStrings 行为。
func (f *FakeEmbed) SetEmbed(fn func(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.embedFn = fn
}

// EmbedStrings 实现 EmbedRole。默认对每条文本生成 dims 维确定向量（元素有限）。
func (f *FakeEmbed) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	f.mu.Lock()
	f.calls++
	fn := f.embedFn
	dims := f.dims
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, texts, opts...)
	}
	out := make([][]float64, len(texts))
	for i, t := range texts {
		v := make([]float64, dims)
		for j := range v {
			// 确定且有限：与文本长度、下标相关，避免全零以便区分
			v[j] = float64(len(t)+i+j+1) * 0.001
		}
		out[i] = v
	}
	return out, nil
}

// Dimensions 实现 EmbedRole。
func (f *FakeEmbed) Dimensions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dims
}

// ModelID 实现 EmbedRole。
func (f *FakeEmbed) ModelID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.modelID
}

// Calls 返回 EmbedStrings 调用次数。
func (f *FakeEmbed) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// FakeRerank 是可配置的 RerankRole fake，返回截断到 topK 的独立副本并附分数。
type FakeRerank struct {
	mu sync.Mutex

	rerankFn func(ctx context.Context, query string, docs []*schema.Document, topK int) ([]*schema.Document, error)
	calls    int
}

// NewFakeRerank 创建默认成功的 RerankRole fake。
func NewFakeRerank() *FakeRerank { return &FakeRerank{} }

// SetRerank 覆盖 Rerank 行为。
func (f *FakeRerank) SetRerank(fn func(ctx context.Context, query string, docs []*schema.Document, topK int) ([]*schema.Document, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rerankFn = fn
}

// Rerank 实现 RerankRole。默认按输入顺序截断到 topK，返回独立副本并附递减分数。
func (f *FakeRerank) Rerank(ctx context.Context, query string, docs []*schema.Document, topK int) ([]*schema.Document, error) {
	f.mu.Lock()
	f.calls++
	fn := f.rerankFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, query, docs, topK)
	}
	if len(docs) == 0 {
		return nil, errors.New("fake rerank: empty docs")
	}
	if topK <= 0 {
		return nil, fmt.Errorf("fake rerank: invalid topK: %d", topK)
	}
	k := topK
	if k > len(docs) {
		k = len(docs)
	}
	out := make([]*schema.Document, 0, k)
	for i := 0; i < k; i++ {
		src := docs[i]
		cp := &schema.Document{ID: src.ID, Content: src.Content, MetaData: copyMeta(src.MetaData)}
		out = append(out, cp.WithScore(float64(k-i)/float64(k)))
	}
	return out, nil
}

// Calls 返回 Rerank 调用次数。
func (f *FakeRerank) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func copyMeta(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// FakeRegistry 是 ProviderRegistry 的全角色离线实现，经接口注入。
// 各角色字段可由测试替换为定制 fake；未替换时使用 NewFakeRegistry 的默认 fake。
type FakeRegistry struct {
	Chat    ChatRole
	Rewrite ChatRole
	QA      ChatRole
	Extract ChatRole
	Embed   EmbedRole
	Rerank  RerankRole
	Budget  *ProcessBudget // B1: 测试可注入；nil 时 NewRequestBudget 创建不限预算
}

var _ ProviderRegistry = (*FakeRegistry)(nil)

// NewFakeRegistry 创建全角色默认 fake 的 FakeRegistry。
// Chat 与 Extract 共享同一 fake 实例（对应 §3.4.4 的角色别名语义）。
func NewFakeRegistry() *FakeRegistry {
	chat := NewFakeChat()
	return &FakeRegistry{
		Chat:    chat,
		Rewrite: NewFakeChat(),
		QA:      NewFakeChat(),
		Extract: chat,
		Embed:   NewFakeEmbed(1024, "fake-embed"),
		Rerank:  NewFakeRerank(),
	}
}

func (r *FakeRegistry) GetChat(context.Context) (ChatRole, error) {
	return r.roleOrErr(r.Chat, "chat")
}
func (r *FakeRegistry) GetRewrite(context.Context) (ChatRole, error) {
	return r.roleOrErr(r.Rewrite, "rewrite")
}
func (r *FakeRegistry) GetQA(context.Context) (ChatRole, error) {
	return r.roleOrErr(r.QA, "qa")
}
func (r *FakeRegistry) GetExtract(context.Context) (ChatRole, error) {
	return r.roleOrErr(r.Extract, "extract")
}

func (r *FakeRegistry) roleOrErr(role ChatRole, name string) (ChatRole, error) {
	if role == nil {
		return nil, fmt.Errorf("%w: %s", ErrRoleNotConfigured, name)
	}
	return role, nil
}

func (r *FakeRegistry) GetEmbedder(context.Context) (EmbedRole, error) {
	if r.Embed == nil {
		return nil, fmt.Errorf("%w: embed", ErrRoleNotConfigured)
	}
	return r.Embed, nil
}

func (r *FakeRegistry) GetReranker(context.Context) (RerankRole, error) {
	if r.Rerank == nil {
		return nil, fmt.Errorf("%w: rerank", ErrRoleNotConfigured)
	}
	return r.Rerank, nil
}

// NewRequestBudget 从绑定的 ProcessBudget 创建请求级预算（B1）。
// Budget 为 nil 时创建不限额度预算。
func (r *FakeRegistry) NewRequestBudget(ctx context.Context, opts ...BudgetOption) (*RequestBudget, error) {
	pb := r.Budget
	if pb == nil {
		// 不限额度进程预算
		pb, _ = NewProcessBudget(0, 0, 0, 0)
	}
	return pb.NewRequestBudget(ctx, opts...)
}
