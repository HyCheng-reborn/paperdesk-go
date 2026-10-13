package rag

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

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

// --- 行为测试 ---

// TestInit_FailsWithoutConfig 验证无配置时 Init 返回 error 而非 panic。
func TestInit_FailsWithoutConfig(t *testing.T) {
	err := Init()
	if err == nil {
		Reset()
		t.Skip("Init() succeeded unexpectedly (config exists); cleaned up")
	}
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after failed Init()")
	}
	if GetRagSvr() != nil {
		t.Fatal("GetRagSvr() should return nil after failed Init()")
	}
}

// TestInitWith_VectorStoreError 验证 VectorStore 创建失败时不发布。
func TestInitWith_VectorStoreError(t *testing.T) {
	defer resetState()

	factory := &fakeVectorStoreFactory{createErr: errors.New("vs error")}
	cfg := &vector.Config{Type: "qdrant", IndexName: "test"}

	err := InitWith(cfg, factory)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "NewVectorStore") {
		t.Fatalf("expected NewVectorStore error, got: %v", err)
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after VectorStore error")
	}
}

// TestInitWith_NilFactory_UsesDefault 验证 nil 工厂使用默认（会因无配置失败）。
func TestInitWith_NilFactory_UsesDefault(t *testing.T) {
	defer resetState()

	cfg := &vector.Config{Type: "qdrant", IndexName: "test"}
	err := InitWith(cfg, nil)
	if err == nil {
		Reset()
		t.Skip("InitWith() with nil factory succeeded unexpectedly; cleaned up")
	}
	// 使用默认工厂会尝试真实连接，预期失败
	if IsInitialized() {
		t.Fatal("should not be initialized")
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

// TestInitWith_RepeatCall_Error 验证重复初始化返回错误。
func TestInitWith_RepeatCall_Error(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{}
	factory := &fakeVectorStoreFactory{vs: fakeVS}
	cfg := &vector.Config{Type: "qdrant", IndexName: "test"}

	// 模拟已初始化状态
	ragMu.Lock()
	ragInit = true
	ragMu.Unlock()

	err := InitWith(cfg, factory)
	if err == nil {
		t.Fatal("expected error on repeat call")
	}
	if !strings.Contains(err.Error(), "already called") {
		t.Fatalf("expected 'already called' error, got: %v", err)
	}
	if factory.createCount != 0 {
		t.Fatal("factory should not be called on repeat init")
	}
}

// TestInitWith_NilConfig_ReadsFromGoFrame 验证 nil 配置时从 GoFrame 读取（会因无配置失败）。
func TestInitWith_NilConfig_ReadsFromGoFrame(t *testing.T) {
	defer resetState()

	fakeVS := &fakeVectorStore{}
	factory := &fakeVectorStoreFactory{vs: fakeVS}

	err := InitWith(nil, factory)
	if err == nil {
		Reset()
		t.Skip("InitWith(nil, factory) succeeded unexpectedly; cleaned up")
	}
	// 无配置文件时应该返回配置错误
	if factory.createCount != 0 {
		t.Fatal("factory should not be called when config is missing")
	}
}
