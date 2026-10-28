package rag

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/gogf/gf/v2/container/gvar"
	"github.com/wangle201210/go-rag/server/core"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// --- fake 实现 ---

// fakeVectorStore 测试用的 VectorStore 实现
type fakeVectorStore struct {
	mu             sync.Mutex
	closeErr       error
	closeCount     int
	createIndexErr error
}

func (f *fakeVectorStore) CreateIndex(ctx context.Context, indexName string, fp *vector.Fingerprint) error {
	return f.createIndexErr
}
func (f *fakeVectorStore) IndexExists(ctx context.Context, indexName string) (bool, error) {
	return true, nil
}
func (f *fakeVectorStore) DeleteDocument(ctx context.Context, indexName, documentID string) error {
	return nil
}
func (f *fakeVectorStore) GetKnowledgeBaseList(ctx context.Context, indexName string) ([]string, error) {
	return nil, nil
}
func (f *fakeVectorStore) SearchDocuments(ctx context.Context, req *vector.SearchRequest) (*vector.SearchResponse, error) {
	return &vector.SearchResponse{}, nil
}
func (f *fakeVectorStore) WriteFingerprint(ctx context.Context, indexName string, fp *vector.Fingerprint, resourceID string) error {
	return nil
}
func (f *fakeVectorStore) ReadFingerprint(ctx context.Context, indexName string) (*vector.Fingerprint, string, error) {
	return &vector.Fingerprint{
		Version:          vector.FingerprintVersion,
		EmbeddingModelID: "test-model",
		Dimensions:       1024,
		ProviderName:     "test",
		Similarity:       "cosine",
	}, "", nil
}
func (f *fakeVectorStore) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCount++
	return f.closeErr
}

// fakeVectorStoreFactory 测试用的 VectorStoreFactory 实现
type fakeVectorStoreFactory struct {
	vs          *fakeVectorStore
	createErr   error
	createCount int
}

func (f *fakeVectorStoreFactory) Create(cfg *vector.Config) (vector.VectorStore, error) {
	f.createCount++
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.vs, nil
}

// fakeRagFactory 测试用的 RagFactory 实现
type fakeRagFactory struct {
	createErr   error
	createCount int
}

func (f *fakeRagFactory) Create(cfg RagConfig, vs vector.VectorStore) (*core.Rag, error) {
	f.createCount++
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &core.Rag{}, nil
}

// --- 辅助函数 ---

// resetState 重置包级状态，用于测试隔离
func resetState() {
	ragMu.Lock()
	defer ragMu.Unlock()
	if vectorStore != nil {
		_ = vectorStore.Close()
	}
	ragSvr = nil
	vectorStore = nil
	ragInit = false
}

// validRagConfig 返回一个有效的最小 RagConfig 用于测试
func validRagConfig() RagConfig {
	return RagConfig{
		VectorCfg:       vector.Config{Type: "qdrant", IndexName: "test"},
		EmbeddingAPIKey: "test-key",
		BaseURL:         "http://test",
		EmbeddingModel:  "test-embed",
		ChatModel:       "test-chat",
	}
}

// --- 前置条件测试 ---

// TestGetRagSvrBeforeInit 验证未初始化时 GetRagSvr 返回 nil。
func TestGetRagSvrBeforeInit(t *testing.T) {
	svr := GetRagSvr()
	if svr != nil {
		t.Fatal("GetRagSvr() should return nil before Init()")
	}
}

// TestIsInitializedBeforeCall 验证未初始化时 IsInitialized 返回 false。
func TestIsInitializedBeforeCall(t *testing.T) {
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false before Init()")
	}
}

// TestResetBeforeInit 验证未初始化时 Reset 是安全的 no-op。
func TestResetBeforeInit(t *testing.T) {
	err := Reset()
	if err != nil {
		t.Fatalf("Reset() before Init() should return nil, got %v", err)
	}
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after Reset()")
	}
	if GetRagSvr() != nil {
		t.Fatal("GetRagSvr() should return nil after Reset()")
	}
}

// --- 行为测试（全部使用 fake，不调用现场 Init/factory/config） ---

// TestInit_ConfigError_BeforeFactory 验证配置错误在工厂调用之前被检测到。
func TestInit_ConfigError_BeforeFactory(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{}
	vsFactory := &fakeVectorStoreFactory{vs: fakeVS}
	ragFctory := &fakeRagFactory{}

	// 空配置 → 配置错误
	cfg := RagConfig{} // 所有字段为零值

	err := InitWith(cfg, vsFactory, ragFctory)
	if err == nil {
		t.Fatal("expected config error")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected config error, got: %v", err)
	}
	// 工厂不应被调用
	if vsFactory.createCount != 0 {
		t.Fatal("VS factory should not be called on config error")
	}
	if ragFctory.createCount != 0 {
		t.Fatal("RAG factory should not be called on config error")
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after config error")
	}
}

// TestInit_VSFactoryError 验证 VectorStore 工厂失败时不发布。
func TestInit_VSFactoryError(t *testing.T) {
	defer resetState()

	vsFactory := &fakeVectorStoreFactory{createErr: errors.New("vs error")}
	ragFctory := &fakeRagFactory{}
	cfg := validRagConfig()

	err := InitWith(cfg, vsFactory, ragFctory)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "NewVectorStore") {
		t.Fatalf("expected NewVectorStore error, got: %v", err)
	}
	if ragFctory.createCount != 0 {
		t.Fatal("RAG factory should not be called when VS factory fails")
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after VS factory error")
	}
}

// TestInit_RagFactoryError_ClosesVS 验证 RagFactory 失败时关闭已创建的 VectorStore。
func TestInit_RagFactoryError_ClosesVS(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{}
	vsFactory := &fakeVectorStoreFactory{vs: fakeVS}
	ragFctory := &fakeRagFactory{createErr: errors.New("rag error")}
	cfg := validRagConfig()

	err := InitWith(cfg, vsFactory, ragFctory)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "rag error") {
		t.Fatalf("expected rag factory error, got: %v", err)
	}
	// VectorStore 应被关闭
	if fakeVS.closeCount != 1 {
		t.Fatalf("expected VS to be closed after rag factory error, got %d closes", fakeVS.closeCount)
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after rag factory error")
	}
}

// TestInit_NilFactory_Error 验证 nil 工厂返回错误。
func TestInit_NilFactory_Error(t *testing.T) {
	defer resetState()

	cfg := validRagConfig()

	// nil VS factory
	err := InitWith(cfg, nil, &fakeRagFactory{})
	if err == nil {
		t.Fatal("expected error with nil VS factory")
	}
	if !strings.Contains(err.Error(), "must not be nil") {
		t.Fatalf("expected 'must not be nil' error, got: %v", err)
	}

	// nil RAG factory
	err = InitWith(cfg, &fakeVectorStoreFactory{vs: &fakeVectorStore{}}, nil)
	if err == nil {
		t.Fatal("expected error with nil RAG factory")
	}
	if !strings.Contains(err.Error(), "must not be nil") {
		t.Fatalf("expected 'must not be nil' error, got: %v", err)
	}
}

// TestInit_Success_Publishes 验证成功初始化后全局状态已发布。
func TestInit_Success_Publishes(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{}
	vsFactory := &fakeVectorStoreFactory{vs: fakeVS}
	ragFctory := &fakeRagFactory{}
	cfg := validRagConfig()

	err := InitWith(cfg, vsFactory, ragFctory)
	if err != nil {
		t.Fatalf("InitWith() unexpected error: %v", err)
	}

	if !IsInitialized() {
		t.Fatal("IsInitialized() should return true after successful InitWith()")
	}
	if GetRagSvr() == nil {
		t.Fatal("GetRagSvr() should return non-nil after successful InitWith()")
	}
	if vsFactory.createCount != 1 {
		t.Fatalf("expected 1 VS factory call, got %d", vsFactory.createCount)
	}
	if ragFctory.createCount != 1 {
		t.Fatalf("expected 1 RAG factory call, got %d", ragFctory.createCount)
	}
}

// TestInit_RepeatCall_Rejected 验证重复初始化被拒绝（通过真实成功，非预设状态）。
func TestInit_RepeatCall_Rejected(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{}
	vsFactory := &fakeVectorStoreFactory{vs: fakeVS}
	ragFctory := &fakeRagFactory{}
	cfg := validRagConfig()

	// 第一次成功
	err := InitWith(cfg, vsFactory, ragFctory)
	if err != nil {
		t.Fatalf("first InitWith() unexpected error: %v", err)
	}

	// 第二次返回 error
	err = InitWith(cfg, vsFactory, ragFctory)
	if err == nil {
		t.Fatal("expected error on repeat call")
	}
	if !strings.Contains(err.Error(), "already called") {
		t.Fatalf("expected 'already called' error, got: %v", err)
	}

	// 工厂调用数不增加
	if vsFactory.createCount != 1 {
		t.Fatalf("VS factory should be called only once, got %d", vsFactory.createCount)
	}
	if ragFctory.createCount != 1 {
		t.Fatalf("RAG factory should be called only once, got %d", ragFctory.createCount)
	}
}

// TestReset_ClosesVectorStore 验证 Reset 实际关闭 VectorStore。
func TestReset_ClosesVectorStore(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{}

	// 直接设置全局变量来模拟部分初始化
	ragMu.Lock()
	vectorStore = fakeVS
	ragInit = true
	ragMu.Unlock()

	err := Reset()
	if err != nil {
		t.Fatalf("Reset() unexpected error: %v", err)
	}
	if fakeVS.closeCount != 1 {
		t.Fatalf("expected 1 close call after Reset, got %d", fakeVS.closeCount)
	}
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after Reset()")
	}
	if vectorStore != nil {
		t.Fatal("vectorStore should be nil after Reset()")
	}
}

// TestReset_VectorStoreCloseError 验证 Reset 返回 Close 错误。
func TestReset_VectorStoreCloseError(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{closeErr: errors.New("close error")}
	ragMu.Lock()
	vectorStore = fakeVS
	ragInit = true
	ragMu.Unlock()

	err := Reset()
	if err == nil {
		t.Fatal("expected error from VectorStore.Close()")
	}
	if !strings.Contains(err.Error(), "close error") {
		t.Fatalf("expected close error, got: %v", err)
	}
}

// --- 配置错误传播测试 ---

// TestInit_EmbeddingModelEmpty 验证空 embedding.model 在工厂调用前被拒绝。
func TestInit_EmbeddingModelEmpty(t *testing.T) {
	defer resetState()

	vsFactory := &fakeVectorStoreFactory{vs: &fakeVectorStore{}}
	ragFctory := &fakeRagFactory{}
	cfg := validRagConfig()
	cfg.EmbeddingModel = ""

	err := InitWith(cfg, vsFactory, ragFctory)
	if err == nil {
		t.Fatal("expected error for empty embedding.model")
	}
	if !strings.Contains(err.Error(), "embedding.model") {
		t.Fatalf("expected embedding.model error, got: %v", err)
	}
	if vsFactory.createCount != 0 {
		t.Fatal("factory should not be called for config error")
	}
}

// TestInit_ChatModelEmpty 验证空 chat.model 在工厂调用前被拒绝。
func TestInit_ChatModelEmpty(t *testing.T) {
	defer resetState()

	vsFactory := &fakeVectorStoreFactory{vs: &fakeVectorStore{}}
	ragFctory := &fakeRagFactory{}
	cfg := validRagConfig()
	cfg.ChatModel = ""

	err := InitWith(cfg, vsFactory, ragFctory)
	if err == nil {
		t.Fatal("expected error for empty chat.model")
	}
	if !strings.Contains(err.Error(), "chat.model") {
		t.Fatalf("expected chat.model error, got: %v", err)
	}
	if vsFactory.createCount != 0 {
		t.Fatal("factory should not be called for config error")
	}
}

// TestInit_VectorTypeEmpty 验证空 vector.type 在工厂调用前被拒绝。
func TestInit_VectorTypeEmpty(t *testing.T) {
	defer resetState()

	vsFactory := &fakeVectorStoreFactory{vs: &fakeVectorStore{}}
	ragFctory := &fakeRagFactory{}
	cfg := validRagConfig()
	cfg.VectorCfg.Type = ""

	err := InitWith(cfg, vsFactory, ragFctory)
	if err == nil {
		t.Fatal("expected error for empty vector.type")
	}
	if !strings.Contains(err.Error(), "vector.type") {
		t.Fatalf("expected vector.type error, got: %v", err)
	}
	if vsFactory.createCount != 0 {
		t.Fatal("factory should not be called for config error")
	}
}

// TestInit_IndexNameEmpty 验证空 vector.indexName 在工厂调用前被拒绝。
func TestInit_IndexNameEmpty(t *testing.T) {
	defer resetState()

	vsFactory := &fakeVectorStoreFactory{vs: &fakeVectorStore{}}
	ragFctory := &fakeRagFactory{}
	cfg := validRagConfig()
	cfg.VectorCfg.IndexName = ""

	err := InitWith(cfg, vsFactory, ragFctory)
	if err == nil {
		t.Fatal("expected error for empty vector.indexName")
	}
	if !strings.Contains(err.Error(), "vector.indexName") {
		t.Fatalf("expected vector.indexName error, got: %v", err)
	}
	if vsFactory.createCount != 0 {
		t.Fatal("factory should not be called for config error")
	}
}

// --- 修复 2: RagFactory 失败时清理错误保留测试 ---

// TestInit_RagFactoryError_PreservesCloseError 验证 RagFactory 失败时 VectorStore Close 错误被保留。
func TestInit_RagFactoryError_PreservesCloseError(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{closeErr: errors.New("vs close error")}
	vsFactory := &fakeVectorStoreFactory{vs: fakeVS}
	ragFctory := &fakeRagFactory{createErr: errors.New("rag create error")}
	cfg := validRagConfig()

	err := InitWith(cfg, vsFactory, ragFctory)
	if err == nil {
		t.Fatal("expected error")
	}

	// 验证包含 rag 创建错误
	if !strings.Contains(err.Error(), "rag create error") {
		t.Fatalf("should contain rag create error, got: %v", err)
	}

	// 验证包含 vs close 错误
	if !strings.Contains(err.Error(), "vs close error") {
		t.Fatalf("should contain vs close error, got: %v", err)
	}

	// 验证 VS 只关闭一次
	if fakeVS.closeCount != 1 {
		t.Fatalf("expected 1 close, got %d", fakeVS.closeCount)
	}
}

// --- 1D-2 readQAConfig 测试 ---

// fakeConfigGetter 实现 configGetter 接口用于测试
type fakeConfigGetter struct {
	data map[string]*gvar.Var
	err  error
}

func (f *fakeConfigGetter) Get(ctx context.Context, key string) (*gvar.Var, error) {
	if f.err != nil {
		return nil, f.err
	}
	if v, ok := f.data[key]; ok {
		return v, nil
	}
	// 缺失 key 返回 IsNil 的 Var
	return gvar.New(nil), nil
}

// TestReadQAConfig_Missing 缺失配置使用默认值
func TestReadQAConfig_Missing(t *testing.T) {
	ctx := context.Background()
	getter := &fakeConfigGetter{data: map[string]*gvar.Var{}}

	enabled, maxConcurrent, err := readQAConfig(ctx, getter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 缺失字段使用默认值
	if enabled != nil {
		t.Errorf("expected nil enabled for missing config, got %v", *enabled)
	}
	if maxConcurrent != 0 {
		t.Errorf("expected 0 maxConcurrent for missing config, got %d", maxConcurrent)
	}
}

// TestReadQAConfig_False 显式 false
func TestReadQAConfig_False(t *testing.T) {
	ctx := context.Background()
	getter := &fakeConfigGetter{data: map[string]*gvar.Var{
		"qa.enabled": gvar.New(false),
	}}

	enabled, _, err := readQAConfig(ctx, getter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enabled == nil {
		t.Fatal("expected non-nil enabled")
	}
	if *enabled != false {
		t.Errorf("expected false, got %v", *enabled)
	}
}

// TestReadQAConfig_True 显式 true
func TestReadQAConfig_True(t *testing.T) {
	ctx := context.Background()
	getter := &fakeConfigGetter{data: map[string]*gvar.Var{
		"qa.enabled": gvar.New(true),
	}}

	enabled, _, err := readQAConfig(ctx, getter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enabled == nil {
		t.Fatal("expected non-nil enabled")
	}
	if *enabled != true {
		t.Errorf("expected true, got %v", *enabled)
	}
}

// TestReadQAConfig_MaxConcurrent_Default 缺失默认为 0
func TestReadQAConfig_MaxConcurrent_Default(t *testing.T) {
	ctx := context.Background()
	getter := &fakeConfigGetter{data: map[string]*gvar.Var{}}

	_, maxConcurrent, err := readQAConfig(ctx, getter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if maxConcurrent != 0 {
		t.Errorf("expected 0, got %d", maxConcurrent)
	}
}

// TestReadQAConfig_MaxConcurrent_Boundary 边界值测试
func TestReadQAConfig_MaxConcurrent_Boundary(t *testing.T) {
	tests := []struct {
		name  string
		value int
		want  int
	}{
		{"negative", -1, -1},
		{"zero", 0, 0},
		{"normal", 4, 4},
		{"max", 32, 32},
		{"over_max", 33, 33},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			getter := &fakeConfigGetter{data: map[string]*gvar.Var{
				"qa.maxConcurrent": gvar.New(tt.value),
			}}

			_, maxConcurrent, err := readQAConfig(ctx, getter)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if maxConcurrent != tt.want {
				t.Errorf("maxConcurrent = %d, want %d", maxConcurrent, tt.want)
			}
		})
	}
}

// TestReadQAConfig_ReadError 读取错误传播
func TestReadQAConfig_ReadError(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("config read failed")
	getter := &fakeConfigGetter{err: sentinel}

	_, _, err := readQAConfig(ctx, getter)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
}

// --- 阻塞 1: Reset 收敛测试 ---

// TestReset_ShutdownTimeout_PreservesDependencies 验证 Shutdown 超时时保留依赖和引用。
func TestReset_ShutdownTimeout_PreservesDependencies(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{}

	blockedRag := &core.Rag{}
	bgRootCtx, bgRootCancel := context.WithCancel(context.Background())

	// 获取实例自身 bgWG 指针
	ragVal := reflect.ValueOf(blockedRag).Elem()
	bgWGField := ragVal.FieldByName("bgWG")
	bgWGPtr := reflect.NewAt(bgWGField.Type(), unsafe.Pointer(bgWGField.UnsafeAddr())).Interface().(*sync.WaitGroup)
	bgWGPtr.Add(1) // 永不 Done → Shutdown 超时

	setRagField(blockedRag, "bgRootCtx", bgRootCtx)
	setRagField(blockedRag, "bgRootCancel", bgRootCancel)
	setRagField(blockedRag, "bgSem", make(chan struct{}, 8))
	setRagField(blockedRag, "done", make(chan struct{}))

	ragMu.Lock()
	ragSvr = blockedRag
	vectorStore = fakeVS
	ragInit = true
	ragMu.Unlock()

	// 清理：释放后台任务并等待真实 Shutdown 完成
	t.Cleanup(func() {
		bgWGPtr.Done()
		select {
		case <-ragDoneChan(blockedRag):
		case <-time.After(5 * time.Second):
			t.Error("blockedRag done not closed during cleanup")
		}
		bgRootCancel()
	})

	// 调用 Reset，内部 Shutdown 会超时（5s）
	resetDone := make(chan struct{})
	var resetErr error
	go func() {
		defer close(resetDone)
		resetErr = Reset()
	}()

	// 等待 Reset 返回（内部 5s 超时）
	select {
	case <-resetDone:
		// Reset 已返回
	case <-time.After(7 * time.Second):
		t.Fatal("Reset() did not return within expected time")
	}

	// 应返回 Shutdown 超时错误
	if resetErr == nil {
		t.Fatal("expected error from Reset() when Shutdown times out")
	}
	if !strings.Contains(resetErr.Error(), "shutdown failed") {
		t.Fatalf("expected shutdown failed error, got: %v", resetErr)
	}

	// 依赖应被保留：vectorStore 未 Close
	if fakeVS.closeCount != 0 {
		t.Fatalf("vectorStore should NOT be closed when Shutdown fails, got %d closes", fakeVS.closeCount)
	}

	// ragSvr 和 ragInit 应被保留
	ragMu.RLock()
	if ragSvr == nil {
		t.Fatal("ragSvr should NOT be nil when Shutdown fails")
	}
	if ragInit != true {
		t.Fatal("ragInit should still be true when Shutdown fails")
	}
	ragMu.RUnlock()
}

// TestReset_ShutdownSuccess_CleansUp 验证 Shutdown 成功时正确清理所有引用。
func TestReset_ShutdownSuccess_CleansUp(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{}

	// 设置全局状态（无 ragSvr，只有 vectorStore）
	// 无 ragSvr 时 Reset 不调用 Shutdown，直接清理
	ragMu.Lock()
	ragSvr = nil
	vectorStore = fakeVS
	ragInit = true
	ragMu.Unlock()

	err := Reset()
	if err != nil {
		t.Fatalf("Reset() unexpected error: %v", err)
	}

	// vectorStore 应被关闭
	if fakeVS.closeCount != 1 {
		t.Fatalf("expected vectorStore to be closed, got %d closes", fakeVS.closeCount)
	}
	// 所有引用应被置空
	ragMu.RLock()
	if ragSvr != nil {
		t.Fatal("ragSvr should be nil after Reset")
	}
	if vectorStore != nil {
		t.Fatal("vectorStore should be nil after Reset")
	}
	if ragInit != false {
		t.Fatal("ragInit should be false after Reset")
	}
	ragMu.RUnlock()
}

// --- 1D-3 readLifecycleConfig 测试 ---

// TestReadLifecycleConfig_Missing 缺失配置使用默认值（0，由 New() 钳位）
func TestReadLifecycleConfig_Missing(t *testing.T) {
	ctx := context.Background()
	getter := &fakeConfigGetter{data: map[string]*gvar.Var{}}

	maxBG, timeout, err := readLifecycleConfig(ctx, getter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if maxBG != 0 {
		t.Errorf("expected 0 maxBG for missing config, got %d", maxBG)
	}
	if timeout != 0 {
		t.Errorf("expected 0 timeout for missing config, got %v", timeout)
	}
}

// TestReadLifecycleConfig_Explicit 显式值
func TestReadLifecycleConfig_Explicit(t *testing.T) {
	ctx := context.Background()
	getter := &fakeConfigGetter{data: map[string]*gvar.Var{
		"rag.maxBackgroundTasks":           gvar.New(16),
		"rag.backgroundTaskTimeoutMinutes": gvar.New(10),
	}}

	maxBG, timeout, err := readLifecycleConfig(ctx, getter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if maxBG != 16 {
		t.Errorf("maxBG = %d, want 16", maxBG)
	}
	if timeout != 10*time.Minute {
		t.Errorf("timeout = %v, want 10m", timeout)
	}
}

// TestReadLifecycleConfig_Boundary 边界值
func TestReadLifecycleConfig_Boundary(t *testing.T) {
	tests := []struct {
		name        string
		maxBG       int
		timeout     int
		wantMaxBG   int
		wantTimeout time.Duration
	}{
		{"negative", -1, -1, -1, 5 * time.Minute}, // 非正分钟数默认 5min
		{"zero", 0, 0, 0, 5 * time.Minute},        // 零值分钟数默认 5min
		{"normal", 8, 5, 8, 5 * time.Minute},
		{"max", 256, 60, 256, 60 * time.Minute},
		{"over_max_bg", 300, 10, 300, 10 * time.Minute},   // maxBG 不在 readLifecycleConfig 钳位，由 New() 处理
		{"over_max_timeout", 8, 120, 8, 60 * time.Minute}, // 超过 60 分钟钳位到 60
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			getter := &fakeConfigGetter{data: map[string]*gvar.Var{
				"rag.maxBackgroundTasks":           gvar.New(tt.maxBG),
				"rag.backgroundTaskTimeoutMinutes": gvar.New(tt.timeout),
			}}

			maxBG, timeout, err := readLifecycleConfig(ctx, getter)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if maxBG != tt.wantMaxBG {
				t.Errorf("maxBG = %d, want %d", maxBG, tt.wantMaxBG)
			}
			if timeout != tt.wantTimeout {
				t.Errorf("timeout = %v, want %v", timeout, tt.wantTimeout)
			}
		})
	}
}

// TestReadLifecycleConfig_ReadError 读取错误传播
func TestReadLifecycleConfig_ReadError(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("config read failed")
	getter := &fakeConfigGetter{err: sentinel}

	_, _, err := readLifecycleConfig(ctx, getter)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
}

// TestReadLifecycleConfig_TypeError 非整数类型返回错误
func TestReadLifecycleConfig_TypeError(t *testing.T) {
	ctx := context.Background()

	// maxBackgroundTasks 非整数
	getter := &fakeConfigGetter{data: map[string]*gvar.Var{
		"rag.maxBackgroundTasks": gvar.New("not-an-int"),
	}}
	_, _, err := readLifecycleConfig(ctx, getter)
	if err == nil {
		t.Fatal("expected error for non-int maxBackgroundTasks")
	}
	if !strings.Contains(err.Error(), "must be int") {
		t.Fatalf("expected 'must be int' error, got: %v", err)
	}

	// backgroundTaskTimeoutMinutes 非整数
	getter2 := &fakeConfigGetter{data: map[string]*gvar.Var{
		"rag.backgroundTaskTimeoutMinutes": gvar.New(3.14),
	}}
	_, _, err = readLifecycleConfig(ctx, getter2)
	if err == nil {
		t.Fatal("expected error for non-int backgroundTaskTimeoutMinutes")
	}
	if !strings.Contains(err.Error(), "must be int") {
		t.Fatalf("expected 'must be int' error, got: %v", err)
	}
}

// --- 阻塞 1: Reset 并发与实例身份测试 ---

// setRagField 使用 reflect+unsafe 设置 Rag 未导出字段
func setRagField(rag *core.Rag, name string, val interface{}) {
	ragVal := reflect.ValueOf(rag).Elem()
	f := ragVal.FieldByName(name)
	reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Set(reflect.ValueOf(val))
}

// ragDoneChan 通过 reflect+unsafe 获取 Rag 实例的 done channel（不复制）。
// 用于等待真实 Shutdown 完成。
func ragDoneChan(rag *core.Rag) <-chan struct{} {
	ragVal := reflect.ValueOf(rag).Elem()
	doneField := ragVal.FieldByName("done")
	ptr := reflect.NewAt(doneField.Type(), unsafe.Pointer(doneField.UnsafeAddr()))
	return ptr.Elem().Interface().(chan struct{})
}

// newTestRagWithShutdown 创建一个可控制 Shutdown 行为的 Rag 用于测试。
// 获取实例自身 bgWG 指针，在同一对象 Add/Done，不复制。
// done 只能由真实 Rag.Shutdown 关闭，不手工 close。
// 返回 Rag 和用于释放 Shutdown 等待的函数。
func newTestRagWithShutdown() (*core.Rag, chan struct{}) {
	blockedRag := &core.Rag{}
	bgRootCtx, bgRootCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	release := make(chan struct{})

	// 获取实例自身 bgWG 指针（通过 reflect 获取 Rag 的 bgWG 字段地址）
	ragVal := reflect.ValueOf(blockedRag).Elem()
	bgWGField := ragVal.FieldByName("bgWG")
	bgWGPtr := reflect.NewAt(bgWGField.Type(), unsafe.Pointer(bgWGField.UnsafeAddr())).Interface().(*sync.WaitGroup)

	bgWGPtr.Add(1)

	setRagField(blockedRag, "bgRootCtx", bgRootCtx)
	setRagField(blockedRag, "bgRootCancel", bgRootCancel)
	setRagField(blockedRag, "bgSem", make(chan struct{}, 8))
	setRagField(blockedRag, "done", done)

	// 后台 goroutine 等待 release 后 Done（在实例自身的 bgWG 上）
	go func() {
		<-release
		bgWGPtr.Done()
	}()

	return blockedRag, release
}

// TestReset_Concurrent_NoRaceOrPanic 验证 32 轮 x 64 goroutine 并发 Reset 无 race/panic
func TestReset_Concurrent_NoRaceOrPanic(t *testing.T) {
	defer resetState()

	for round := 0; round < 32; round++ {
		// 每轮设置一个新实例
		fakeVS := &fakeVectorStore{}
		blockedRag, release := newTestRagWithShutdown()

		ragMu.Lock()
		ragSvr = blockedRag
		vectorStore = fakeVS
		ragInit = true
		ragMu.Unlock()

		const n = 64
		var wg sync.WaitGroup
		errCh := make(chan error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := Reset(); err != nil {
					errCh <- err
				}
			}()
		}

		// 释放 Shutdown 等待
		close(release)
		wg.Wait()
		close(errCh)

		// 收集错误（允许部分返回 shutdown failed，因为并发竞争）
		for err := range errCh {
			if !strings.Contains(err.Error(), "shutdown failed") {
				t.Fatalf("round %d: unexpected error: %v", round, err)
			}
		}

		// 等待真实 Shutdown 完成（done close）
		select {
		case <-ragDoneChan(blockedRag):
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: blockedRag done not closed", round)
		}

		// 清理
		resetState()
	}
}

// TestReset_DuringWait_GetRagSvrCallable 验证等待期间 GetRagSvr 仍可调用
func TestReset_DuringWait_GetRagSvrCallable(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{}
	blockedRag, release := newTestRagWithShutdown()

	ragMu.Lock()
	ragSvr = blockedRag
	vectorStore = fakeVS
	ragInit = true
	ragMu.Unlock()

	// 启动 Reset（会阻塞在 Shutdown）
	resetDone := make(chan struct{})
	go func() {
		defer close(resetDone)
		_ = Reset()
	}()

	// 等待 Reset 进入 Shutdown 等待
	time.Sleep(100 * time.Millisecond)

	// GetRagSvr 应仍可调用（不阻塞）
	getDone := make(chan struct{})
	go func() {
		defer close(getDone)
		svr := GetRagSvr()
		// 可能返回 blockedRag（尚未清理）或 nil（已清理）
		_ = svr
	}()

	select {
	case <-getDone:
		// GetRagSvr 返回，未阻塞
	case <-time.After(2 * time.Second):
		t.Fatal("GetRagSvr blocked during Reset wait")
	}

	// 释放并等待 Reset 完成
	close(release)
	select {
	case <-resetDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Reset did not return after release")
	}

	// 等待真实 Shutdown 完成
	select {
	case <-ragDoneChan(blockedRag):
	case <-time.After(5 * time.Second):
		t.Fatal("blockedRag done not closed after release")
	}
}

// TestReset_ShutdownTimeout_PreservesExactObjects 验证超时保留准确对象、状态、资源
func TestReset_ShutdownTimeout_PreservesExactObjects(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{}

	// 创建一个永不完成的 Shutdown（bgWG 永不归零）
	blockedRag := &core.Rag{}
	bgRootCtx, bgRootCancel := context.WithCancel(context.Background())

	// 获取实例自身 bgWG 指针
	ragVal := reflect.ValueOf(blockedRag).Elem()
	bgWGField := ragVal.FieldByName("bgWG")
	bgWGPtr := reflect.NewAt(bgWGField.Type(), unsafe.Pointer(bgWGField.UnsafeAddr())).Interface().(*sync.WaitGroup)
	bgWGPtr.Add(1) // 永不 Done → Shutdown 超时

	setRagField(blockedRag, "bgRootCtx", bgRootCtx)
	setRagField(blockedRag, "bgRootCancel", bgRootCancel)
	setRagField(blockedRag, "bgSem", make(chan struct{}, 8))
	setRagField(blockedRag, "done", make(chan struct{}))

	ragMu.Lock()
	ragSvr = blockedRag
	vectorStore = fakeVS
	ragInit = true
	ragMu.Unlock()

	t.Cleanup(func() {
		bgRootCancel()
	})

	// Reset 应超时返回错误（真实 Shutdown 5s 超时）
	err := Reset()
	if err == nil {
		t.Fatal("expected error from Reset when Shutdown times out")
	}
	if !strings.Contains(err.Error(), "shutdown failed") {
		t.Fatalf("expected 'shutdown failed' error, got: %v", err)
	}

	// 证明已进入等待（bgRootCancel 被 Shutdown 调用）
	select {
	case <-bgRootCtx.Done():
		// 正常：bgRootCtx 已取消
	case <-time.After(1 * time.Second):
		t.Fatal("bgRootCtx should have been cancelled by Shutdown")
	}

	// 验证对象保留：不提前 Close
	if fakeVS.closeCount != 0 {
		t.Fatalf("vectorStore should NOT be closed, got %d closes", fakeVS.closeCount)
	}

	ragMu.RLock()
	if ragSvr != blockedRag {
		t.Fatal("ragSvr should be preserved")
	}
	if vectorStore != fakeVS {
		t.Fatal("vectorStore should be preserved")
	}
	if ragInit != true {
		t.Fatal("ragInit should be preserved")
	}
	ragMu.RUnlock()

	// 释放真实任务，等待 Shutdown 内部 goroutine 完成
	bgWGPtr.Done()
	select {
	case <-ragDoneChan(blockedRag):
		// Shutdown 内部 goroutine 已完成，done 已 close
	case <-time.After(5 * time.Second):
		t.Fatal("blockedRag done not closed after bgWGPtr.Done()")
	}

	// 二次 Reset 成功（真实 Shutdown，done 已 close 立即返回）
	err = Reset()
	if err != nil {
		t.Fatalf("second Reset should succeed: %v", err)
	}
}

// TestReset_AfterTaskCompletion_SecondResetSucceeds 验证任务结束后二次 Reset 成功
func TestReset_AfterTaskCompletion_SecondResetSucceeds(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{}
	blockedRag, release := newTestRagWithShutdown()

	ragMu.Lock()
	ragSvr = blockedRag
	vectorStore = fakeVS
	ragInit = true
	ragMu.Unlock()

	// 第一次 Reset
	close(release) // 先释放，让 Shutdown 成功
	err := Reset()
	if err != nil {
		t.Fatalf("first Reset should succeed: %v", err)
	}

	// 等待真实 Shutdown 完成
	select {
	case <-ragDoneChan(blockedRag):
	case <-time.After(5 * time.Second):
		t.Fatal("blockedRag done not closed after Reset")
	}

	// 验证已清理
	if IsInitialized() {
		t.Fatal("should not be initialized after first Reset")
	}

	// 第二次 Reset（无实例）应成功
	err = Reset()
	if err != nil {
		t.Fatalf("second Reset should succeed: %v", err)
	}
}

// TestReset_ConcurrentInitDoesNotCloseNewInstance 验证等待期间另一调用完成 Reset+Init 后旧调用不关闭新对象。
// 注意：由于无法使用真实 Init（需要真实工厂），换代通过直接修改全局状态模拟。
// 但 Reset 调用本身是真实的，换代检查逻辑是真实执行的。
func TestReset_ConcurrentInitDoesNotCloseNewInstance(t *testing.T) {
	defer resetState()

	// 创建旧实例
	oldVS := &fakeVectorStore{}
	oldRag, oldRelease := newTestRagWithShutdown()

	ragMu.Lock()
	ragSvr = oldRag
	vectorStore = oldVS
	ragInit = true
	ragMu.Unlock()

	// 启动第一个 Reset（会阻塞在 Shutdown）
	reset1Done := make(chan struct{})
	var reset1Err error
	go func() {
		defer close(reset1Done)
		reset1Err = Reset()
	}()

	// 等待 Reset1 进入 Shutdown 等待
	time.Sleep(100 * time.Millisecond)

	// 模拟另一调用完成 Reset+Init：直接修改全局状态
	newVS := &fakeVectorStore{}
	newRag, newRelease := newTestRagWithShutdown()

	ragMu.Lock()
	ragSvr = newRag
	vectorStore = newVS
	ragInit = true
	ragMu.Unlock()

	// 释放旧实例的 Shutdown
	close(oldRelease)

	// 等待第一个 Reset 完成
	select {
	case <-reset1Done:
	case <-time.After(7 * time.Second):
		t.Fatal("first Reset did not return")
	}

	// 第一个 Reset 应返回 nil（旧实例已 Shutdown，但发现换代，不触碰新对象）
	if reset1Err != nil {
		t.Fatalf("first Reset should return nil on generation change, got: %v", reset1Err)
	}

	// 等待旧实例 Shutdown 内部 goroutine 完成（真实 done close）
	select {
	case <-ragDoneChan(oldRag):
		// 旧实例 done 已关闭
	case <-time.After(5 * time.Second):
		t.Fatal("oldRag done not closed after release")
	}

	// 新实例应完好
	ragMu.RLock()
	if ragSvr != newRag {
		t.Fatal("new ragSvr should be preserved")
	}
	if vectorStore != newVS {
		t.Fatal("new vectorStore should be preserved")
	}
	if ragInit != true {
		t.Fatal("ragInit should be true for new instance")
	}
	ragMu.RUnlock()

	// 新实例的 VectorStore 不应被关闭
	if newVS.closeCount != 0 {
		t.Fatalf("new vectorStore should NOT be closed, got %d closes", newVS.closeCount)
	}

	// 清理新实例：新实例未被 Shutdown，先调用 Shutdown 再释放后台任务
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = newRag.Shutdown(shutdownCtx)
	shutdownCancel()
	close(newRelease)
	// 等待新实例 Shutdown 完成
	select {
	case <-ragDoneChan(newRag):
	case <-time.After(5 * time.Second):
		t.Fatal("newRag done not closed during cleanup")
	}
	resetState()
}

// --- C1 Factory Decision-Tree Tests ---
// These tests replicate the factory's C1 decision tree (IndexExists → CreateIndex/Verify)
// using a tracking VectorStore mock. The simulateFactoryC1 function mirrors the exact logic
// from defaultRagFactory.Create (lines 99-162 of retriever.go).

// trackingVectorStore is a fakeVectorStore with configurable behavior and operation tracking.
type trackingVectorStore struct {
	mu               sync.Mutex
	closeErr         error
	closeCount       int
	indexExistsResult bool
	indexExistsErr   error
	createIndexErr   error
	createIndexFP    *vector.Fingerprint // fingerprint passed to CreateIndex
	readFP           *vector.Fingerprint
	readResourceID   string
	readFPErr        error
	operations       []string
}

func (t *trackingVectorStore) CreateIndex(ctx context.Context, indexName string, fp *vector.Fingerprint) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.operations = append(t.operations, "CreateIndex")
	t.createIndexFP = fp
	return t.createIndexErr
}
func (t *trackingVectorStore) IndexExists(ctx context.Context, indexName string) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.operations = append(t.operations, "IndexExists")
	return t.indexExistsResult, t.indexExistsErr
}
func (t *trackingVectorStore) DeleteDocument(ctx context.Context, indexName, documentID string) error {
	return nil
}
func (t *trackingVectorStore) GetKnowledgeBaseList(ctx context.Context, indexName string) ([]string, error) {
	return nil, nil
}
func (t *trackingVectorStore) SearchDocuments(ctx context.Context, req *vector.SearchRequest) (*vector.SearchResponse, error) {
	return &vector.SearchResponse{}, nil
}
func (t *trackingVectorStore) WriteFingerprint(ctx context.Context, indexName string, fp *vector.Fingerprint, resourceID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.operations = append(t.operations, "WriteFingerprint")
	return nil
}
func (t *trackingVectorStore) ReadFingerprint(ctx context.Context, indexName string) (*vector.Fingerprint, string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.operations = append(t.operations, "ReadFingerprint")
	return t.readFP, t.readResourceID, t.readFPErr
}
func (t *trackingVectorStore) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeCount++
	return t.closeErr
}

// simulateFactoryC1 replicates the C1 decision tree from defaultRagFactory.Create.
// It creates a core.Rag via reflection, sets its conf, then follows the same
// IndexExists → CreateIndex/VerifyFingerprint flow as the production factory.
// Returns the Rag, the list of VectorStore operations performed, and any error.
func simulateFactoryC1(ctx context.Context, cfg RagConfig, vs vector.VectorStore) (*core.Rag, []string, error) {
	var err error
	cfg, err = normalizeDimensions(cfg)
	if err != nil {
		return nil, nil, err
	}

	// C1: Check index existence (mirrors defaultRagFactory line 105)
	indexExisted, err := vs.IndexExists(ctx, cfg.VectorCfg.IndexName)
	if err != nil {
		return nil, nil, fmt.Errorf("fingerprint: cannot check index existence: %w", err)
	}

	confForNew := &config.Config{
		IndexName:           cfg.VectorCfg.IndexName,
		APIKey:              cfg.EmbeddingAPIKey,
		BaseURL:             cfg.BaseURL,
		EmbeddingModel:      cfg.EmbeddingModel,
		EmbeddingDimensions: cfg.EmbeddingDimensions,
		ChatModel:           cfg.ChatModel,
	}

	// Create minimal Rag (simulates core.New success)
	rag := &core.Rag{}
	setRagField(rag, "conf", confForNew)

	// C1/C2/C3: Unified controlled creation flow (mirrors defaultRagFactory lines 134-162)
	if !indexExisted {
		fp, fpErr := core.ComputeFingerprintMeta(confForNew)
		if fpErr != nil {
			return nil, nil, fmt.Errorf("compute fingerprint: %w", fpErr)
		}
		createErr := vs.CreateIndex(ctx, cfg.VectorCfg.IndexName, fp)
		if createErr != nil {
			if errors.Is(createErr, vector.ErrIndexAlreadyExists) {
				if verifyErr := rag.VerifyFingerprint(ctx, vs); verifyErr != nil {
					return nil, nil, verifyErr
				}
			} else {
				return nil, nil, fmt.Errorf("fingerprint: controlled creation failed: %w", createErr)
			}
		} else {
			if verifyErr := rag.VerifyFingerprint(ctx, vs); verifyErr != nil {
				return nil, nil, verifyErr
			}
		}
	} else {
		if verifyErr := rag.VerifyFingerprint(ctx, vs); verifyErr != nil {
			return nil, nil, verifyErr
		}
	}

	// Extract operation log from trackingVectorStore
	var ops []string
	if tvs, ok := vs.(*trackingVectorStore); ok {
		tvs.mu.Lock()
		ops = make([]string, len(tvs.operations))
		copy(ops, tvs.operations)
		tvs.mu.Unlock()
	}

	return rag, ops, nil
}

// validFingerprintForTest returns a fingerprint matching what ComputeFingerprintMeta
// produces for validRagConfig() after normalizeDimensions (provider="legacy", similarity="cosine").
func validFingerprintForTest() *vector.Fingerprint {
	fp, _ := vector.NewFingerprint(vector.FingerprintVersion, "test-embed", 1024, "legacy", "cosine")
	return fp
}

// TestFactory_IndexExistsError_Propagated verifies that when IndexExists returns an error,
// the factory propagates it (C1: error is NOT discarded).
func TestFactory_IndexExistsError_Propagated(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("index check failed")
	vs := &trackingVectorStore{
		indexExistsErr: sentinel,
	}
	cfg := validRagConfig()

	_, _, err := simulateFactoryC1(ctx, cfg, vs)
	if err == nil {
		t.Fatal("expected error when IndexExists fails")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected error to wrap sentinel, got: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot check index existence") {
		t.Fatalf("expected 'cannot check index existence' in error, got: %v", err)
	}
}

// TestFactory_NewIndex_CreateAndVerify verifies the full creation path:
// IndexExists=false → CreateIndex(non-nil fingerprint) → VerifyFingerprint → success.
func TestFactory_NewIndex_CreateAndVerify(t *testing.T) {
	ctx := context.Background()
	vs := &trackingVectorStore{
		indexExistsResult: false,
		readFP:           validFingerprintForTest(),
		readResourceID:   "test-uuid-123",
	}
	cfg := validRagConfig()

	rag, ops, err := simulateFactoryC1(ctx, cfg, vs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rag == nil {
		t.Fatal("expected non-nil Rag")
	}

	// CreateIndex should have been called with a non-nil fingerprint
	vs.mu.Lock()
	fp := vs.createIndexFP
	vs.mu.Unlock()
	if fp == nil {
		t.Fatal("CreateIndex should have received a non-nil fingerprint")
	}
	if fp.EmbeddingModelID != "test-embed" {
		t.Errorf("fingerprint model = %q, want %q", fp.EmbeddingModelID, "test-embed")
	}
	if fp.Dimensions != 1024 {
		t.Errorf("fingerprint dimensions = %d, want 1024", fp.Dimensions)
	}

	// Verify call sequence
	expectedOps := []string{"IndexExists", "CreateIndex", "ReadFingerprint"}
	if !reflect.DeepEqual(ops, expectedOps) {
		t.Errorf("operations = %v, want %v", ops, expectedOps)
	}
}

// TestFactory_ExistingIndex_VerifyOnly verifies that when the index already exists,
// CreateIndex is NOT called and only verification is performed.
func TestFactory_ExistingIndex_VerifyOnly(t *testing.T) {
	ctx := context.Background()
	vs := &trackingVectorStore{
		indexExistsResult: true,
		readFP:           validFingerprintForTest(),
		readResourceID:   "test-uuid-456",
	}
	cfg := validRagConfig()

	rag, ops, err := simulateFactoryC1(ctx, cfg, vs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rag == nil {
		t.Fatal("expected non-nil Rag")
	}

	// CreateIndex should NOT have been called
	vs.mu.Lock()
	createCalled := vs.createIndexFP != nil
	vs.mu.Unlock()
	if createCalled {
		t.Fatal("CreateIndex should NOT be called when index already exists")
	}

	// Verify call sequence: IndexExists → ReadFingerprint (via VerifyFingerprint)
	expectedOps := []string{"IndexExists", "ReadFingerprint"}
	if !reflect.DeepEqual(ops, expectedOps) {
		t.Errorf("operations = %v, want %v", ops, expectedOps)
	}
}

// TestFactory_ExistingIndex_NoFingerprint_Fails verifies that when the index exists
// but has no fingerprint, the factory returns an error (refuses to proceed).
func TestFactory_ExistingIndex_NoFingerprint_Fails(t *testing.T) {
	ctx := context.Background()
	vs := &trackingVectorStore{
		indexExistsResult: true,
		readFPErr:        vector.ErrFingerprintMissing,
	}
	cfg := validRagConfig()

	_, _, err := simulateFactoryC1(ctx, cfg, vs)
	if err == nil {
		t.Fatal("expected error when fingerprint is missing on existing index")
	}
	if !strings.Contains(err.Error(), "no fingerprint") {
		t.Fatalf("expected 'no fingerprint' in error, got: %v", err)
	}
}

// TestFactory_ConcurrentCreate_GoesToVerification verifies that when CreateIndex
// returns ErrIndexAlreadyExists (concurrent creation race), the factory falls through
// to the verification path and succeeds if verification passes.
func TestFactory_ConcurrentCreate_GoesToVerification(t *testing.T) {
	ctx := context.Background()
	vs := &trackingVectorStore{
		indexExistsResult: false,
		createIndexErr:   vector.ErrIndexAlreadyExists,
		readFP:           validFingerprintForTest(),
		readResourceID:   "test-uuid-789",
	}
	cfg := validRagConfig()

	rag, ops, err := simulateFactoryC1(ctx, cfg, vs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rag == nil {
		t.Fatal("expected non-nil Rag")
	}

	// Verify call sequence: IndexExists → CreateIndex (race) → ReadFingerprint (verify)
	expectedOps := []string{"IndexExists", "CreateIndex", "ReadFingerprint"}
	if !reflect.DeepEqual(ops, expectedOps) {
		t.Errorf("operations = %v, want %v", ops, expectedOps)
	}
}

// --- D1: Factory flow tests ---

// TestD1_CreateIndexError_Propagated verifies that when CreateIndex returns a
// non-ErrIndexAlreadyExists error, the factory propagates it immediately.
func TestD1_CreateIndexError_Propagated(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("disk full")
	vs := &trackingVectorStore{
		indexExistsResult: false,
		createIndexErr:   sentinel,
	}
	cfg := validRagConfig()

	_, _, err := simulateFactoryC1(ctx, cfg, vs)
	if err == nil {
		t.Fatal("expected error from CreateIndex")
	}
	if !strings.Contains(err.Error(), "controlled creation failed") {
		t.Errorf("expected 'controlled creation failed' error, got: %v", err)
	}
}

// TestD1_CreateIndexBeforeVerify_Order verifies that CreateIndex happens before
// ReadFingerprint (verification) in the factory flow.
func TestD1_CreateIndexBeforeVerify_Order(t *testing.T) {
	ctx := context.Background()
	vs := &trackingVectorStore{
		indexExistsResult: false,
		readFP:           validFingerprintForTest(),
		readResourceID:   "test-uuid-order",
	}
	cfg := validRagConfig()

	_, ops, err := simulateFactoryC1(ctx, cfg, vs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// D1: CreateIndex must come before ReadFingerprint
	createIdx := -1
	readIdx := -1
	for i, op := range ops {
		if op == "CreateIndex" && createIdx == -1 {
			createIdx = i
		}
		if op == "ReadFingerprint" && readIdx == -1 {
			readIdx = i
		}
	}
	if createIdx == -1 {
		t.Fatal("CreateIndex should have been called")
	}
	if readIdx == -1 {
		t.Fatal("ReadFingerprint should have been called")
	}
	if createIdx >= readIdx {
		t.Errorf("D1: CreateIndex (idx=%d) should happen before ReadFingerprint (idx=%d)", createIdx, readIdx)
	}
}

// TestD1_ExistingIndex_NoCreateIndex verifies that when the index already exists,
// the factory skips CreateIndex and goes directly to verification.
func TestD1_ExistingIndex_NoCreateIndex(t *testing.T) {
	ctx := context.Background()
	vs := &trackingVectorStore{
		indexExistsResult: true,
		readFP:           validFingerprintForTest(),
		readResourceID:   "test-uuid-existing",
	}
	cfg := validRagConfig()

	_, ops, err := simulateFactoryC1(ctx, cfg, vs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// D1: Existing index → no CreateIndex, just IndexExists → ReadFingerprint
	for _, op := range ops {
		if op == "CreateIndex" {
			t.Error("D1: CreateIndex should NOT be called when index already exists")
		}
	}
	expectedOps := []string{"IndexExists", "ReadFingerprint"}
	if !reflect.DeepEqual(ops, expectedOps) {
		t.Errorf("operations = %v, want %v", ops, expectedOps)
	}
}

// TestD1_ConcurrentCreate_VerifyReadsResourceID verifies that after concurrent
// creation detection (ErrIndexAlreadyExists), the verification path reads the
// resourceID from storage (D4 binding).
func TestD1_ConcurrentCreate_VerifyReadsResourceID(t *testing.T) {
	ctx := context.Background()
	vs := &trackingVectorStore{
		indexExistsResult: false,
		createIndexErr:   vector.ErrIndexAlreadyExists,
		readFP:           validFingerprintForTest(),
		readResourceID:   "concurrent-resource-uuid",
	}
	cfg := validRagConfig()

	rag, ops, err := simulateFactoryC1(ctx, cfg, vs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rag == nil {
		t.Fatal("expected non-nil Rag")
	}

	// Verify the verification path was taken
	expectedOps := []string{"IndexExists", "CreateIndex", "ReadFingerprint"}
	if !reflect.DeepEqual(ops, expectedOps) {
		t.Errorf("operations = %v, want %v", ops, expectedOps)
	}
}

// ---------------------------------------------------------------------------
// 2C-3: InitWith conditional validation tests
// ---------------------------------------------------------------------------

// TestInitWith_ProviderEmbeddingLegacyEmpty verifies that when Provider Embedding
// is configured, legacy EmbeddingModel can be empty without error.
func TestInitWith_ProviderEmbeddingLegacyEmpty(t *testing.T) {
	resetState()
	defer resetState()

	cfg := validRagConfig()
	cfg.EmbeddingModel = "" // legacy empty
	cfg.Provider = &config.ProviderConfig{
		Name:                "openai",
		EmbeddingModel:      "text-embedding-3-large",
		EmbeddingDimensions: 768,
	}

	vsFactory := &fakeVectorStoreFactory{vs: &fakeVectorStore{}}
	ragFctory := &fakeRagFactory{}
	err := InitWith(cfg, vsFactory, ragFctory)
	if err != nil {
		t.Fatalf("expected no error when Provider Embedding is set, got: %v", err)
	}
}

// TestInitWith_ProviderChatLegacyEmpty verifies that when Provider Chat is
// configured, legacy ChatModel can be empty without error.
func TestInitWith_ProviderChatLegacyEmpty(t *testing.T) {
	resetState()
	defer resetState()

	cfg := validRagConfig()
	cfg.ChatModel = "" // legacy empty
	cfg.Provider = &config.ProviderConfig{
		Name:      "openai",
		ChatModel: "gpt-4",
	}

	vsFactory := &fakeVectorStoreFactory{vs: &fakeVectorStore{}}
	ragFctory := &fakeRagFactory{}
	err := InitWith(cfg, vsFactory, ragFctory)
	if err != nil {
		t.Fatalf("expected no error when Provider Chat is set, got: %v", err)
	}
}

// TestInitWith_NeitherProviderNorLegacyEmbedding verifies that when neither
// Provider nor legacy Embedding is configured, InitWith returns error.
func TestInitWith_NeitherProviderNorLegacyEmbedding(t *testing.T) {
	resetState()
	defer resetState()

	cfg := validRagConfig()
	cfg.EmbeddingModel = "" // legacy empty
	// No Provider

	vsFactory := &fakeVectorStoreFactory{vs: &fakeVectorStore{}}
	ragFctory := &fakeRagFactory{}
	err := InitWith(cfg, vsFactory, ragFctory)
	if err == nil {
		t.Fatal("expected error when neither Provider nor legacy Embedding is configured")
	}
	if !strings.Contains(err.Error(), "embedding.model") {
		t.Fatalf("expected embedding.model error, got: %v", err)
	}
}

// TestInitWith_NeitherProviderNorLegacyChat verifies that when neither Provider
// nor legacy Chat is configured, InitWith returns error.
func TestInitWith_NeitherProviderNorLegacyChat(t *testing.T) {
	resetState()
	defer resetState()

	cfg := validRagConfig()
	cfg.ChatModel = "" // legacy empty
	// No Provider

	vsFactory := &fakeVectorStoreFactory{vs: &fakeVectorStore{}}
	ragFctory := &fakeRagFactory{}
	err := InitWith(cfg, vsFactory, ragFctory)
	if err == nil {
		t.Fatal("expected error when neither Provider nor legacy Chat is configured")
	}
	if !strings.Contains(err.Error(), "chat.model") {
		t.Fatalf("expected chat.model error, got: %v", err)
	}
}

// TestInitWith_ProviderEmbeddingNoChat verifies that Provider Embedding without
// Provider Chat and without legacy ChatModel returns error.
func TestInitWith_ProviderEmbeddingNoChat(t *testing.T) {
	resetState()
	defer resetState()

	cfg := validRagConfig()
	cfg.ChatModel = "" // legacy empty
	cfg.Provider = &config.ProviderConfig{
		Name:                "openai",
		EmbeddingModel:      "text-embedding-3-large",
		EmbeddingDimensions: 768,
		// No ChatModel
	}

	vsFactory := &fakeVectorStoreFactory{vs: &fakeVectorStore{}}
	ragFctory := &fakeRagFactory{}
	err := InitWith(cfg, vsFactory, ragFctory)
	if err == nil {
		t.Fatal("expected error when no chat model is available")
	}
	if !strings.Contains(err.Error(), "chat.model") {
		t.Fatalf("expected chat.model error, got: %v", err)
	}
}
