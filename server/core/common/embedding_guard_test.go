package common

import (
	"context"
	"errors"
	"testing"

	"github.com/wangle201210/go-rag/server/core/config"
)

// ---------------------------------------------------------------------------
// B2: Embedding guard 测试
// ---------------------------------------------------------------------------

// TestEmbedding_ProductionGate 验证新 Embedding profile 在 2C guard 不存在时 fail closed。
func TestEmbedding_ProductionGate(t *testing.T) {
	conf := &config.Config{
		APIKey:         "test-key",
		BaseURL:        "http://localhost",
		EmbeddingModel: "text-embedding-3-large",
		Provider: &config.ProviderConfig{
			Name:           "bailian",
			EmbeddingModel: "text-embedding-v4", // 明确配置了新 Embedding profile
		},
	}

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected guard error for new embedding profile, got nil")
	}
	if !errors.Is(err, ErrEmbeddingGuardNotReady) {
		t.Errorf("expected ErrEmbeddingGuardNotReady, got: %v", err)
	}
}

// TestEmbedding_UnconfiguredFallsBack 验证未配置 Embedding 角色继续旧路径。
func TestEmbedding_UnconfiguredFallsBack(t *testing.T) {
	conf := &config.Config{
		APIKey:         "test-key",
		BaseURL:        "http://localhost",
		EmbeddingModel: "text-embedding-3-large",
		// Provider 为 nil → 继续旧路径
	}

	// 旧路径会尝试连接 http://localhost，这里只验证不返回 guard 错误
	_, err := NewEmbedding(context.Background(), conf)
	// 可能因为连接失败而报错，但不应该是 guard 错误
	if errors.Is(err, ErrEmbeddingGuardNotReady) {
		t.Fatal("should not return guard error when Provider is nil")
	}
}

// TestEmbedding_ProviderWithoutEmbedModel 验证 Provider 存在但 EmbeddingModel 为空时继续旧路径。
func TestEmbedding_ProviderWithoutEmbedModel(t *testing.T) {
	conf := &config.Config{
		APIKey:         "test-key",
		BaseURL:        "http://localhost",
		EmbeddingModel: "text-embedding-3-large",
		Provider: &config.ProviderConfig{
			Name: "bailian",
			// EmbeddingModel 为空 → 继续旧路径
		},
	}

	_, err := NewEmbedding(context.Background(), conf)
	if errors.Is(err, ErrEmbeddingGuardNotReady) {
		t.Fatal("should not return guard error when Provider.EmbeddingModel is empty")
	}
}
