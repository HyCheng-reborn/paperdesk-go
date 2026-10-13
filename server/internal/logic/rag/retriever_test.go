package rag

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/wangle201210/go-rag/server/core"
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

func (f *fakeVectorStore) CreateIndex(ctx context.Context, indexName string) error {
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
		VectorCfg:      vector.Config{Type: "qdrant", IndexName: "test"},
		EmbeddingAPIKey: "test-key",
		BaseURL:        "http://test",
		EmbeddingModel: "test-embed",
		ChatModel:      "test-chat",
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
