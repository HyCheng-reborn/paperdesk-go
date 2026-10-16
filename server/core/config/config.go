package config

import (
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
	return &Config{
		Client:       x.Client,
		QdrantClient: x.QdrantClient,
		IndexName:    x.IndexName,
		// embedding 时使用
		APIKey:          x.APIKey,
		BaseURL:         x.BaseURL,
		EmbeddingModel:  x.EmbeddingModel,
		ChatModel:       x.ChatModel,
		QAEnabled:       x.QAEnabled,
		QAMaxConcurrent: x.QAMaxConcurrent,
	}
}
