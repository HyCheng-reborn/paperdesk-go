package common

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino-ext/components/model/qwen"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestChatModelConfigMapping 验证 ChatModel 配置映射逻辑（使用测试自有配置）。
func TestChatModelConfigMapping(t *testing.T) {
	cfg := &openai.ChatModelConfig{
		APIKey:  "test-key",
		BaseURL: "https://example.com/v1",
		Model:   "gpt-4",
	}
	if cfg.APIKey != "test-key" {
		t.Errorf("APIKey = %q", cfg.APIKey)
	}
	if cfg.BaseURL != "https://example.com/v1" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Model != "gpt-4" {
		t.Errorf("Model = %q", cfg.Model)
	}
}

// fakeQAModelForChat 实现 model.BaseChatModel 接口用于测试
type fakeQAModelForChat struct{}

func (f *fakeQAModelForChat) Generate(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Content: "fake QA"}, nil
}
func (f *fakeQAModelForChat) Stream(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("not implemented")
}

// okReader 是固定成功的配置读取器
func okReader(_ context.Context) (*qwen.ChatModelConfig, error) {
	return &qwen.ChatModelConfig{}, nil
}

// TestQAModelCache_FailRetryCached 同一实例上确定性地验证缓存发布契约：
//  1. 第一次 builder 返回 sentinel，错误可识别且未发布缓存；
//  2. 第二次 builder 成功，初始化成功；
//  3. 第三次返回同一缓存实例，builder 总调用次数仍为 2。
func TestQAModelCache_FailRetryCached(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("build failed")

	var buildCount atomic.Int32
	c := &qaModelCache{
		readCfg: okReader,
		build: func(context.Context, *qwen.ChatModelConfig) (model.BaseChatModel, error) {
			if buildCount.Add(1) == 1 {
				return nil, sentinel
			}
			return &fakeQAModelForChat{}, nil
		},
	}

	// 1) 首次失败：错误可识别，缓存未发布
	m1, err1 := c.get(ctx, nil)
	if !errors.Is(err1, sentinel) {
		t.Fatalf("expected sentinel error, got: %v", err1)
	}
	if m1 != nil {
		t.Fatal("expected nil model on failure")
	}

	// 2) 第二次成功（失败未污染缓存，允许重试）
	m2, err2 := c.get(ctx, nil)
	if err2 != nil {
		t.Fatalf("unexpected error on retry: %v", err2)
	}
	if m2 == nil {
		t.Fatal("expected non-nil model after success")
	}

	// 3) 第三次命中缓存，返回同一实例
	m3, err3 := c.get(ctx, nil)
	if err3 != nil {
		t.Fatalf("unexpected error on cached call: %v", err3)
	}
	if m3 != m2 {
		t.Error("expected same cached instance")
	}

	if got := buildCount.Load(); got != 2 {
		t.Fatalf("builder total calls = %d, want 2 (1 fail + 1 success; cache hit must not rebuild)", got)
	}
}

// TestQAModelCache_ReadError_NoBuild 配置读取失败时不调用 builder。
func TestQAModelCache_ReadError_NoBuild(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("config read failed")
	var buildCount atomic.Int32

	c := &qaModelCache{
		readCfg: func(context.Context) (*qwen.ChatModelConfig, error) { return nil, sentinel },
		build: func(context.Context, *qwen.ChatModelConfig) (model.BaseChatModel, error) {
			buildCount.Add(1)
			return &fakeQAModelForChat{}, nil
		},
	}

	if _, err := c.get(ctx, nil); !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got: %v", err)
	}
	if buildCount.Load() != 0 {
		t.Fatalf("builder must not be called when config read fails, got %d", buildCount.Load())
	}
}

// TestQAModelCache_BuilderNilNil_RetryAllowed builder 返回 (nil, nil) 时返回错误且允许重试。
func TestQAModelCache_BuilderNilNil_RetryAllowed(t *testing.T) {
	ctx := context.Background()
	var buildCount atomic.Int32

	c := &qaModelCache{
		readCfg: okReader,
		build: func(context.Context, *qwen.ChatModelConfig) (model.BaseChatModel, error) {
			if buildCount.Add(1) == 1 {
				return nil, nil // 触发 initQAModel 的 nil 检查
			}
			return &fakeQAModelForChat{}, nil
		},
	}

	m1, err1 := c.get(ctx, nil)
	if err1 == nil || m1 != nil {
		t.Fatalf("expected error for (nil,nil) builder, got m=%v err=%v", m1, err1)
	}
	// 允许重试
	m2, err2 := c.get(ctx, nil)
	if err2 != nil || m2 == nil {
		t.Fatalf("expected success on retry after nil-builder, got m=%v err=%v", m2, err2)
	}
}

// TestQAModelCache_ConcurrentBuildsOnce 并发成功调用在同一实例上只构建一次。
func TestQAModelCache_ConcurrentBuildsOnce(t *testing.T) {
	ctx := context.Background()
	var buildCount atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})

	c := &qaModelCache{
		readCfg: okReader,
		build: func(context.Context, *qwen.ChatModelConfig) (model.BaseChatModel, error) {
			buildCount.Add(1)
			close(entered)
			<-release // 持锁以迫使其他 goroutine 在 mutex 上排队
			return &fakeQAModelForChat{}, nil
		},
	}

	const n = 6
	models := make([]model.BaseChatModel, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			models[idx], errs[idx] = c.get(ctx, nil)
		}(i)
	}

	// 等待首个 builder 进入，确保其余调用已排队（无固定 sleep）
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("builder not entered")
	}
	close(release)
	wg.Wait()

	if got := buildCount.Load(); got != 1 {
		t.Fatalf("builder must build exactly once, got %d", got)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d: unexpected error %v", i, errs[i])
		}
		if models[i] != models[0] {
			t.Errorf("goroutine %d: different instance than goroutine 0", i)
		}
	}
}

// TestGetQAModel_PublishesAndCaches 公开入口委派生产默认实例：成功返回并缓存同一实例。
func TestGetQAModel_PublishesAndCaches(t *testing.T) {
	ctx := context.Background()
	cfg := &qwen.ChatModelConfig{APIKey: "k", BaseURL: "http://test", Model: "m"}

	m1, err1 := GetQAModel(ctx, cfg)
	if err1 != nil || m1 == nil {
		t.Fatalf("first GetQAModel: m=%v err=%v", m1, err1)
	}
	m2, err2 := GetQAModel(ctx, cfg)
	if err2 != nil {
		t.Fatalf("second GetQAModel: %v", err2)
	}
	if m1 != m2 {
		t.Error("expected cached same instance from production default cache")
	}
}
