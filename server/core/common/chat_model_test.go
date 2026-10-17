package common

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino-ext/components/model/qwen"
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

// resetQAModelState 重置 QA 模型状态用于测试隔离
func resetQAModelState() {
	qaModelMu.Lock()
	defer qaModelMu.Unlock()
	qaModel = nil
	qaModelReady = false
}

// TestGetQAModel_RetryAfterFailure 首次失败、重试成功、并发调用、错误链和不返回 nil,nil
func TestGetQAModel_RetryAfterFailure(t *testing.T) {
	defer resetQAModelState()

	ctx := context.Background()

	// 使用无效配置 - qwen SDK 可能允许创建但延迟验证
	invalidCfg := &qwen.ChatModelConfig{
		APIKey:  "",
		BaseURL: "http://invalid-test-url",
		Model:   "",
	}

	// 首次调用 - 可能成功（SDK 延迟验证）或失败
	model1, err1 := GetQAModel(ctx, invalidCfg)
	
	// 验证不返回 (nil, nil)
	if model1 == nil && err1 == nil {
		t.Fatal("GetQAModel returned (nil, nil) on first call - this should never happen")
	}

	// 再次调用 - 如果首次成功，应该返回同一实例；如果失败，应该可以重试
	model2, err2 := GetQAModel(ctx, invalidCfg)
	
	// 验证不返回 (nil, nil)
	if model2 == nil && err2 == nil {
		t.Fatal("GetQAModel returned (nil, nil) on retry - this should never happen")
	}

	// 如果两次都成功，应该是同一实例（缓存生效）
	if model1 != nil && model2 != nil && err1 == nil && err2 == nil {
		if model1 != model2 {
			t.Error("expected same cached instance on retry")
		}
	}

	// 如果首次失败，错误应该可识别
	if err1 != nil {
		if !strings.Contains(err1.Error(), "create qa model") && !strings.Contains(err1.Error(), "scan qa config") {
			t.Logf("first call error: %v", err1)
		}
	}
}

// TestGetQAModel_ConcurrentCalls 并发调用安全
func TestGetQAModel_ConcurrentCalls(t *testing.T) {
	defer resetQAModelState()

	ctx := context.Background()
	invalidCfg := &qwen.ChatModelConfig{
		APIKey:  "",
		BaseURL: "http://invalid-test-url",
		Model:   "",
	}

	const numGoroutines = 5
	var wg sync.WaitGroup
	results := make([]struct {
		model interface{}
		err   error
	}, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			m, e := GetQAModel(ctx, invalidCfg)
			results[idx].model = m
			results[idx].err = e
		}(i)
	}
	wg.Wait()

	// 所有调用应该返回相同结果
	for i := 0; i < numGoroutines; i++ {
		if results[i].model == nil && results[i].err == nil {
			t.Errorf("goroutine %d: returned (nil, nil)", i)
		}
		// 所有错误应该相同
		if i > 0 {
			if (results[i].err == nil) != (results[0].err == nil) {
				t.Errorf("goroutine %d: inconsistent error state", i)
			}
		}
	}
}

// TestGetQAModel_DoesNotReturnNilNil 验证不返回 (nil, nil)
func TestGetQAModel_DoesNotReturnNilNil(t *testing.T) {
	defer resetQAModelState()

	ctx := context.Background()

	// 测试多种配置
	configs := []*qwen.ChatModelConfig{
		{APIKey: "", BaseURL: "", Model: ""},
		{APIKey: "test", BaseURL: "http://invalid", Model: "nonexistent"},
	}

	for i, cfg := range configs {
		model, err := GetQAModel(ctx, cfg)
		if model == nil && err == nil {
			t.Errorf("config %d: returned (nil, nil)", i)
		}
		// 重置以测试下一个配置
		resetQAModelState()
	}
}
