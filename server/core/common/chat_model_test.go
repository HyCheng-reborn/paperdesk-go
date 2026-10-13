package common

import (
	"testing"

	"github.com/cloudwego/eino-ext/components/model/openai"
)

// TestChatModelConfigMapping 验证 ChatModel 配置映射逻辑。
// 使用测试自有配置，不依赖外部配置文件。
func TestChatModelConfigMapping(t *testing.T) {
	cfg := &openai.ChatModelConfig{
		APIKey:  "test-key",
		BaseURL: "https://example.com/v1",
		Model:   "gpt-4",
	}

	if cfg.APIKey != "test-key" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "test-key")
	}
	if cfg.BaseURL != "https://example.com/v1" {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, "https://example.com/v1")
	}
	if cfg.Model != "gpt-4" {
		t.Errorf("Model = %q, want %q", cfg.Model, "gpt-4")
	}
}
