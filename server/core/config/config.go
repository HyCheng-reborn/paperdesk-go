package config

import (
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/qdrant/go-client/qdrant"
)

type Config struct {
	Client       *elasticsearch.Client // ES 客户端
	QdrantClient *qdrant.Client        // Qdrant 客户端
	IndexName    string                // index name / collection name
	// embedding 时使用
	APIKey         string
	BaseURL        string
	EmbeddingModel string
	ChatModel      string

	// QA 生成配置
	QAEnabled       *bool // nil = 默认启用（向后兼容）
	QAMaxConcurrent int   // 0 或负数 = 默认 4；>32 钳位到 32

	// 后台任务生命周期配置
	MaxBackgroundTasks    int           // 默认 8，<=0 默认，>256 钳位到 256
	BackgroundTaskTimeout time.Duration // 默认 5min，>60min 钳位到 60min

	// Stage 2B: Provider 配置（可选，nil 时走旧路径）
	Provider *ProviderConfig
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
}

func (x *Config) GetChatModelConfig() *openai.ChatModelConfig {
	if x == nil {
		return nil
	}
	return &openai.ChatModelConfig{
		APIKey:  x.APIKey,
		BaseURL: x.BaseURL,
		Model:   x.ChatModel,
	}
}

func (x *Config) Copy() *Config {
	cp := &Config{
		Client:       x.Client,
		QdrantClient: x.QdrantClient,
		IndexName:    x.IndexName,
		// embedding 时使用
		APIKey:                x.APIKey,
		BaseURL:               x.BaseURL,
		EmbeddingModel:        x.EmbeddingModel,
		ChatModel:             x.ChatModel,
		QAEnabled:             x.QAEnabled,
		QAMaxConcurrent:       x.QAMaxConcurrent,
		MaxBackgroundTasks:    x.MaxBackgroundTasks,
		BackgroundTaskTimeout: x.BackgroundTaskTimeout,
	}
	if x.Provider != nil {
		p := *x.Provider
		cp.Provider = &p
	}
	return cp
}
