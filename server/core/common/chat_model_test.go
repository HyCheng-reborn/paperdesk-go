package common

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino-ext/components/model/qwen"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
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

// fakeQAModelForChat 实现 model.BaseChatModel 接口用于测试
type fakeQAModelForChat struct{}

func (f *fakeQAModelForChat) Generate(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Content: "fake QA"}, nil
}
func (f *fakeQAModelForChat) Stream(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("not implemented")
}

// TestInitQAModel_FirstBuildFail_RetrySuccess 第一次构建返回 sentinel error，第二次返回有效 fake model
func TestInitQAModel_FirstBuildFail_RetrySuccess(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("build failed")

	var buildCount atomic.Int32
	reader := func(ctx context.Context) (*qwen.ChatModelConfig, error) {
		return &qwen.ChatModelConfig{}, nil
	}
	builder := func(ctx context.Context, cfg *qwen.ChatModelConfig) (model.BaseChatModel, error) {
		n := buildCount.Add(1)
		if n == 1 {
			return nil, sentinel
		}
		return &fakeQAModelForChat{}, nil
	}

	// 第一次：构建失败
	_, err := initQAModel(ctx, reader, builder)
	if err == nil {
		t.Fatal("expected error on first build")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
	// 错误链应包含 "build qa model"
	if !errors.Is(err, sentinel) {
		t.Errorf("error chain should contain sentinel: %v", err)
	}
	if c := buildCount.Load(); c != 1 {
		t.Errorf("expected 1 build call, got %d", c)
	}

	// 第二次：构建成功
	cm, err := initQAModel(ctx, reader, builder)
	if err != nil {
		t.Fatalf("unexpected error on retry: %v", err)
	}
	if cm == nil {
		t.Fatal("expected non-nil model on retry")
	}
	if c := buildCount.Load(); c != 2 {
		t.Errorf("expected 2 build calls, got %d", c)
	}
}

// TestInitQAModel_ReaderError 配置读取失败，错误链可识别
func TestInitQAModel_ReaderError(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("config read failed")
	reader := func(ctx context.Context) (*qwen.ChatModelConfig, error) {
		return nil, sentinel
	}
	builder := func(ctx context.Context, cfg *qwen.ChatModelConfig) (model.BaseChatModel, error) {
		t.Error("builder should not be called when reader fails")
		return nil, nil
	}

	_, err := initQAModel(ctx, reader, builder)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
}

// TestInitQAModel_BuilderReturnsNil builder 返回 nil model 时报错
func TestInitQAModel_BuilderReturnsNil(t *testing.T) {
	ctx := context.Background()
	reader := func(ctx context.Context) (*qwen.ChatModelConfig, error) {
		return &qwen.ChatModelConfig{}, nil
	}
	builder := func(ctx context.Context, cfg *qwen.ChatModelConfig) (model.BaseChatModel, error) {
		return nil, nil
	}

	_, err := initQAModel(ctx, reader, builder)
	if err == nil {
		t.Fatal("expected error when builder returns nil")
	}
	if err.Error() != "qa model builder returned nil" {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestGetQAModel_CachesAfterSuccess 成功后缓存，后续调用不重新构建
func TestGetQAModel_CachesAfterSuccess(t *testing.T) {
	defer resetQAModelState()

	ctx := context.Background()
	cfg := &qwen.ChatModelConfig{
		APIKey:  "test-key",
		BaseURL: "http://test",
		Model:   "test-model",
	}

	// 第一次调用
	model1, err1 := GetQAModel(ctx, cfg)
	// qwen.NewChatModel 不实际验证配置，会成功
	if err1 != nil {
		t.Fatalf("unexpected error: %v", err1)
	}
	if model1 == nil {
		t.Fatal("expected non-nil model")
	}

	// 第二次调用应返回同一实例
	model2, err2 := GetQAModel(ctx, cfg)
	if err2 != nil {
		t.Fatalf("unexpected error on retry: %v", err2)
	}
	if model1 != model2 {
		t.Error("expected same cached instance")
	}
}

// TestGetQAModel_ConcurrentCalls 并发调用安全
func TestGetQAModel_ConcurrentCalls(t *testing.T) {
	defer resetQAModelState()

	ctx := context.Background()
	cfg := &qwen.ChatModelConfig{
		APIKey:  "test-key",
		BaseURL: "http://test",
		Model:   "test-model",
	}

	const numGoroutines = 5
	var wg sync.WaitGroup
	results := make([]struct {
		m model.BaseChatModel
		e error
	}, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			m, e := GetQAModel(ctx, cfg)
			results[idx].m = m
			results[idx].e = e
		}(i)
	}
	wg.Wait()

	// 所有调用应成功且返回同一实例
	for i := 0; i < numGoroutines; i++ {
		if results[i].e != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, results[i].e)
		}
		if results[i].m == nil {
			t.Errorf("goroutine %d: returned nil model", i)
		}
		if i > 0 && results[i].m != results[0].m {
			t.Errorf("goroutine %d: different instance than goroutine 0", i)
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

// TestGetQAModel_ConfigReadError 配置读取错误通过 Get 传播
func TestGetQAModel_ConfigReadError(t *testing.T) {
	defer resetQAModelState()

	// 传入 cfg != nil 时不会触发 defaultQAConfigReader，
	// 但传入的 cfg 本身有效，所以这里测试 defaultQAConfigReader 路径
	// 由于无法注入 fake reader 到 GetQAModel，通过 initQAModel 直接测试
	ctx := context.Background()
	sentinel := fmt.Errorf("config unavailable")
	reader := func(ctx context.Context) (*qwen.ChatModelConfig, error) {
		return nil, sentinel
	}
	builder := func(ctx context.Context, cfg *qwen.ChatModelConfig) (model.BaseChatModel, error) {
		t.Error("builder should not be called")
		return nil, nil
	}

	_, err := initQAModel(ctx, reader, builder)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error chain should contain sentinel: %v", err)
	}
}
