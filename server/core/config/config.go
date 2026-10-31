package config

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/gogf/gf/v2/container/gvar"
	"github.com/qdrant/go-client/qdrant"
	"github.com/wangle201210/go-rag/server/core/providers"
)

type Config struct {
	Client       *elasticsearch.Client // ES 客户端
	QdrantClient *qdrant.Client        // Qdrant 客户端
	IndexName    string                // index name / collection name
	// Explicit deployment attestation; false by default. This is not a CAS.
	ManagedResourceLifecycle bool
	// Captures the verified concrete target; installed before graph construction.
	VectorIOGuard func(context.Context) error
	// P1-3: 由 vector.BindPhysicalTarget 生成的不可伪造物理目标绑定。
	// 保存为 any 以避免 config ↔ vector 的导入环；消费方（core/common）必须
	// 类型断言为 *vector.TargetBinding 并校验名称/资源/fingerprint，
	// 零值或任意回调不能替代。
	PhysicalBinding any
	// embedding 时使用
	APIKey              string
	BaseURL             string
	EmbeddingDimensions int
	EmbeddingModel      string
	ChatModel           string
	// P2-1: legacy Chat 使用独立配置，不得复用 embedding 的 endpoint/凭据。
	ChatAPIKey  string
	ChatBaseURL string

	// QA 生成配置
	QAEnabled       *bool // nil = 默认启用（向后兼容）
	QAMaxConcurrent int   // 0 或负数 = 默认 4；>32 钳位到 32

	// 后台任务生命周期配置
	MaxBackgroundTasks    int           // 默认 8，<=0 默认，>256 钳位到 256
	BackgroundTaskTimeout time.Duration // 默认 5min，>60min 钳位到 60min

	// Stage 2B: Provider 配置（可选，nil 时走旧路径）
	Provider *ProviderConfig

	// B5: 显式传入本次构建的 Registry，供 core.New 初始化时使用。
	// 不通过全局变量获取，避免初始化顺序依赖。
	Registry providers.ProviderRegistry
}

// ProviderConfig 是 Stage 2B 的 Provider 配置块。
// 定义百炼/OpenAI 等 Provider 的连接信息、模型 ID 与维度。
type ProviderConfig struct {
	Name                string `yaml:"name"`                 // bailian / openai
	BaseURL             string `yaml:"base_url"`             // https://dashscope.aliyuncs.com/compatible-mode/v1
	APIKey              string `yaml:"api_key"`              // 或从环境变量读取
	ChatModel           string `yaml:"chat_model"`           // qwen-plus
	RewriteModel        string `yaml:"rewrite_model"`        // qwen-plus
	QAModel             string `yaml:"qa_model"`             // qwen-plus
	EmbeddingModel      string `yaml:"embedding_model"`      // text-embedding-v4
	EmbeddingDimensions int    `yaml:"embedding_dimensions"` // 1024
	RerankModel         string `yaml:"rerank_model"`         // qwen3-rerank
	RerankURL           string `yaml:"rerank_url"`           // https://dashscope.aliyuncs.com/compatible-api/v1/reranks

	// B1: 预算配置（可选，<=0 表示不限）
	MaxRequests     int64 `yaml:"max_requests"`      // 最大请求数（0=不限）
	MaxInputTokens  int64 `yaml:"max_input_tokens"`  // 最大输入 token（0=不限）
	MaxOutputTokens int64 `yaml:"max_output_tokens"` // 最大输出 token（0=不限）
	MaxConcurrency  int   `yaml:"max_concurrency"`   // 最大并发（0=不限）

	// 2C-3: 模型是否接受 dimensions 请求参数（本地配置声明，非真实能力证明）。
	// YAML 严格布尔解析；缺失默认 false（不下发 dimensions 参数）。
	SupportsDimensionsRequest bool `yaml:"supports_dimensions_request"`
}

func (x *Config) GetChatModelConfig() *openai.ChatModelConfig {
	if x == nil {
		return nil
	}
	return &openai.ChatModelConfig{
		APIKey:  x.ChatAPIKey,
		BaseURL: x.ChatBaseURL,
		Model:   x.ChatModel,
	}
}

func (x *Config) Copy() *Config {
	cp := &Config{
		Client:                   x.Client,
		QdrantClient:             x.QdrantClient,
		IndexName:                x.IndexName,
		ManagedResourceLifecycle: x.ManagedResourceLifecycle,
		VectorIOGuard:            x.VectorIOGuard,
		PhysicalBinding:          x.PhysicalBinding,
		// embedding 时使用
		APIKey:                x.APIKey,
		BaseURL:               x.BaseURL,
		EmbeddingModel:        x.EmbeddingModel,
		EmbeddingDimensions:   x.EmbeddingDimensions,
		ChatModel:             x.ChatModel,
		ChatAPIKey:            x.ChatAPIKey,
		ChatBaseURL:           x.ChatBaseURL,
		QAEnabled:             x.QAEnabled,
		QAMaxConcurrent:       x.QAMaxConcurrent,
		MaxBackgroundTasks:    x.MaxBackgroundTasks,
		BackgroundTaskTimeout: x.BackgroundTaskTimeout,
		// B5: Registry 字段复制
		Registry: x.Registry,
	}
	if x.Provider != nil {
		p := *x.Provider
		cp.Provider = &p
	}
	return cp
}

// ValidateStrictBoolField checks that a YAML config field is a proper boolean.
// GoFrame's gconv loosely converts strings like "true", "nonsense" and numbers like 1 to bool.
// This function rejects non-bool raw types at the configuration boundary.
// Missing/absent → no error (defaults to false). true → true, false → false.
//
// P2-3: an explicit YAML null and an absent key both surface as a nil value from
// Get, so presence is resolved through the parent mapping: a key that exists with
// a nil value is an invalid boolean, not a missing default.
func ValidateStrictBoolField(ctx context.Context, getter interface {
	Get(ctx context.Context, key string) (*gvar.Var, error)
}, fieldPath string) error {
	v, err := getter.Get(ctx, fieldPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", fieldPath, err)
	}
	if v != nil && !v.IsNil() {
		return validateStrictBoolValue(fieldPath, v.Val())
	}
	// Value is absent or explicitly null; only the parent mapping can tell them apart.
	present, raw, err := fieldPresentInParent(ctx, getter, fieldPath)
	if err != nil {
		return err
	}
	if !present {
		return nil // absent → default false, no error
	}
	return validateStrictBoolValue(fieldPath, raw)
}

func validateStrictBoolValue(fieldPath string, raw any) error {
	if _, ok := raw.(bool); !ok {
		return fmt.Errorf("%s must be a boolean (true/false), got %T: %v", fieldPath, raw, raw)
	}
	return nil
}

// fieldPresentInParent reports whether fieldPath exists in its parent mapping and
// returns the raw value. present=false means the key is truly missing.
func fieldPresentInParent(ctx context.Context, getter interface {
	Get(ctx context.Context, key string) (*gvar.Var, error)
}, fieldPath string) (present bool, raw any, err error) {
	idx := strings.LastIndex(fieldPath, ".")
	if idx <= 0 || idx == len(fieldPath)-1 {
		// Top-level paths have no parent mapping to consult; treat nil as absent.
		return false, nil, nil
	}
	parentPath, fieldName := fieldPath[:idx], fieldPath[idx+1:]
	pv, err := getter.Get(ctx, parentPath)
	if err != nil {
		return false, nil, fmt.Errorf("read %s: %w", parentPath, err)
	}
	if pv == nil || pv.IsNil() {
		return false, nil, nil
	}
	m := pv.Map()
	raw, ok := m[fieldName]
	if !ok {
		return false, nil, nil
	}
	return true, raw, nil
}
