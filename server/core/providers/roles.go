// Package providers 定义 Stage 2 的模型角色契约、不可变 ProviderProfile、
// 共享预算执行核心与生产共享适配链。
//
// 本包为 2A 纯新增实施：不修改既有调用方，兼容层与业务接线留给 2B/2C。
// 角色接口签名锁定 Eino v0.6.0（Generate/Stream 使用 model.Option，
// Stream 返回 *schema.StreamReader[*schema.Message]）。
package providers

import (
	"context"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ChatRole 是 Chat/Rewrite/QA/Extract 角色共用的底层能力接口。
//
// 设计决定（stage2-plan §3.4.2）：Extract 先作为 ChatRole 的角色标识（alias），
// 与 Chat 共享同一模型配置和底层实例，但通过不同角色接口访问。
// 签名与 Eino v0.6.0 的 model.BaseChatModel 完全一致，适配层零转换开销。
type ChatRole interface {
	Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error)
	Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error)
}

// EmbedRole 是 Embedding 角色接口，采用 EmbedStrings 与 Eino embedding.Embedder 对齐
// （设计决定 §3.4.2：不拆分 EmbedDocuments/EmbedQuery），并在 Eino 类型之上
// 增加维度与模型身份声明，供 2C 的索引 fingerprint 使用。
type EmbedRole interface {
	EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error)
	// Dimensions 返回模型输出维度；由 ProviderProfile 声明，非运行时探测。
	Dimensions() int
	// ModelID 返回模型身份标识（model + version），供 fingerprint 记录。
	ModelID() string
}

// RerankRole 是 Rerank 角色接口，签名与 core/rerank.Rerank.Rerank 一致，
// 以便直接复用 Stage 1B-1 已验收的校验链（输入隔离、结果校验、响应体限制、错误链）。
type RerankRole interface {
	Rerank(ctx context.Context, query string, docs []*schema.Document, topK int) ([]*schema.Document, error)
}

// Capabilities 声明 Provider 支持的最小能力集（stage2-plan §3.4.3）。
// 在 Profile 构造时确定，不可变；不假设所有 Provider 均支持上述能力。
type Capabilities struct {
	Stream           bool // 是否支持流式
	StructuredOutput bool // 是否支持结构化输出（JSON Schema / tool calling）
	ToolCalling      bool // 是否支持 tool calling
	Usage            bool // 是否返回 usage 信息
}

// 编译期断言：ChatRole 与 Eino model.BaseChatModel 双向兼容，
// EmbedRole 的 EmbedStrings 与 embedding.Embedder 签名一致。
// 这保证 2A 的角色适配是纯委托，不改变 Stage 1 的 Eino 语义。
var (
	_ model.BaseChatModel = (ChatRole)(nil)
	_ embedding.Embedder  = (embedStringsOnly)(nil)
)

// embedStringsOnly 仅用于编译期校验 EmbedRole 的 EmbedStrings 与
// embedding.Embedder 签名一致（EmbedRole 额外增加 Dimensions/ModelID）。
type embedStringsOnly interface {
	EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error)
}
