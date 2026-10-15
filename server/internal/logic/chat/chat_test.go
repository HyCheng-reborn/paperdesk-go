package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/wangle201210/go-rag/server/internal/dao"
	database "github.com/wangle201210/go-rag/server/internal/dao/db"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// --- fake 实现 ---

// fakeChatModel 测试用的 ChatModel 实现
type fakeChatModel struct{}

func (f *fakeChatModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: "assistant", Content: "test"}, nil
}
func (f *fakeChatModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}

// fakeChatModelFactory 测试用的 ChatModelFactory 实现
type fakeChatModelFactory struct {
	cm          model.BaseChatModel
	createErr   error
	createCount int
}

func (f *fakeChatModelFactory) Create(apiKey, baseURL, modelName string) (model.BaseChatModel, error) {
	f.createCount++
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.cm, nil
}

// fakeDatabase 测试用的 Database 实现（用于初始化 dao）
type fakeDatabase struct {
	mu           sync.Mutex
	connectCount int
	pingCount    int
	closeCount   int
	migrateCount int
	dsn          string
	gormDB       *gorm.DB // 可注入的 gormDB，nil 时返回 nil
}

func (f *fakeDatabase) Connect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCount++
	return nil
}
func (f *fakeDatabase) Ping() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pingCount++
	return nil
}
func (f *fakeDatabase) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCount++
	return nil
}
func (f *fakeDatabase) AutoMigrate() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.migrateCount++
	return nil
}
func (f *fakeDatabase) DSN() string { return f.dsn }
func (f *fakeDatabase) GormDB() *gorm.DB {
	return f.gormDB
}

// fakeDatabaseFactory 测试用的 DatabaseFactory 实现
type fakeDatabaseFactory struct {
	db          *fakeDatabase
	createErr   error
	createCount int
}

func (f *fakeDatabaseFactory) Create(cfg database.Config) (database.Database, error) {
	f.createCount++
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.db, nil
}

// fakeConfig 测试用的 ConfigProvider 实现
type fakeConfig struct {
	cfg *database.Config
	err error
}

func (f *fakeConfig) GetDBConfig() (*database.Config, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.cfg, nil
}

// --- 辅助函数 ---

// resetChatState 重置 chat 包级状态，用于测试隔离
func resetChatState() {
	chatMu.Lock()
	defer chatMu.Unlock()
	chat = nil
	chatInit = false
}

// resetDAOState 重置 dao 包级状态，用于测试隔离
func resetDAOState() {
	_ = dao.Close()
}

// initDAOForTest 使用 fake 初始化 dao（用于需要 dao 已初始化的测试）
// 返回的 fakeDatabase 包含真实的内存 SQLite gormDB
func initDAOForTest(t *testing.T) func() {
	t.Helper()
	// 创建内存 SQLite 作为 gormDB
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test sqlite: %v", err)
	}
	fakeDB := &fakeDatabase{dsn: "test-dsn", gormDB: gormDB}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	if err := dao.InitDB(cfg, factory); err != nil {
		t.Fatalf("failed to init DAO for test: %v", err)
	}
	return func() {
		resetDAOState()
		sqlDB, _ := gormDB.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}
}

// --- 前置条件测试 ---

// TestGetChatBeforeInit 验证未初始化时 GetChat 返回 nil。
func TestGetChatBeforeInit(t *testing.T) {
	c := GetChat()
	if c != nil {
		t.Fatal("GetChat() should return nil before Init()")
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
	Reset()
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after Reset()")
	}
	if GetChat() != nil {
		t.Fatal("GetChat() should return nil after Reset()")
	}
}

// --- 行为测试（全部使用 fake，不调用现场 Init） ---

// TestInit_FailsWithoutDAO 验证 dao 未初始化时 InitWith 返回 error（在工厂调用之前）。
func TestInit_FailsWithoutDAO(t *testing.T) {
	defer resetChatState()
	// 确保 dao 未初始化
	resetDAOState()

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	factory := &fakeChatModelFactory{cm: &fakeChatModel{}}

	err := InitWith(cfg, factory)
	if err == nil {
		t.Fatal("expected error when dao not initialized")
	}
	if !strings.Contains(err.Error(), "dao") {
		t.Fatalf("expected dao error, got: %v", err)
	}
	// 工厂不应被调用（DAO 检查在工厂之前）
	if factory.createCount != 0 {
		t.Fatal("factory should not be called when dao not initialized")
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after dao error")
	}
}

// TestInitWith_ConfigError 验证配置为空时返回错误。
func TestInitWith_ConfigError(t *testing.T) {
	defer resetChatState()

	cfg := &ChatConfig{APIKey: "", BaseURL: "", Model: ""}
	err := InitWith(cfg, nil)
	if err == nil {
		t.Fatal("expected error with empty config")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected config error, got: %v", err)
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after config error")
	}
}

// TestInitWith_ChatModelError 验证 ChatModel 创建失败时不发布。
func TestInitWith_ChatModelError(t *testing.T) {
	defer resetChatState()
	defer resetDAOState()

	// 需要先初始化 dao（因为 DAO 检查在工厂之前）
	fakeDB := &fakeDatabase{dsn: "test"}
	daoFactory := &fakeDatabaseFactory{db: fakeDB}
	daoCfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}
	if err := dao.InitDB(daoCfg, daoFactory); err != nil {
		t.Fatalf("failed to init DAO: %v", err)
	}

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	factory := &fakeChatModelFactory{createErr: errors.New("model error")}

	err := InitWith(cfg, factory)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "chat model failed") {
		t.Fatalf("expected chat model error, got: %v", err)
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after chat model error")
	}
}

// TestInitWith_RepeatCall_Rejected 验证重复初始化被拒绝（通过真实成功，非预设状态）。
func TestInitWith_RepeatCall_Rejected(t *testing.T) {
	defer resetChatState()
	defer resetDAOState()

	// 初始化 dao
	cleanupDAO := initDAOForTest(t)
	defer cleanupDAO()

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	factory := &fakeChatModelFactory{cm: &fakeChatModel{}}

	// 第一次成功
	err := InitWith(cfg, factory)
	if err != nil {
		t.Fatalf("first InitWith() unexpected error: %v", err)
	}

	// 第二次返回 error
	err = InitWith(cfg, factory)
	if err == nil {
		t.Fatal("expected error on repeat call")
	}
	if !strings.Contains(err.Error(), "already called") {
		t.Fatalf("expected 'already called' error, got: %v", err)
	}
	// 工厂只调用一次
	if factory.createCount != 1 {
		t.Fatalf("factory should only be called once, got %d", factory.createCount)
	}
}

// TestInitWith_DAOError_BeforeFactory 验证 DAO 错误在工厂调用之前被检测。
func TestInitWith_DAOError_BeforeFactory(t *testing.T) {
	defer resetChatState()
	// 确保 dao 未初始化
	resetDAOState()

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	factory := &fakeChatModelFactory{cm: &fakeChatModel{}}

	err := InitWith(cfg, factory)
	if err == nil {
		t.Fatal("expected error when dao not initialized")
	}
	// 工厂不应被调用
	if factory.createCount != 0 {
		t.Fatal("factory should not be called when dao not initialized")
	}
	if !strings.Contains(err.Error(), "dao") {
		t.Fatalf("expected dao error, got: %v", err)
	}
}

// --- 协调测试 ---

// TestInit_Order_DAOFirst 验证 chat 在 DAO 未初始化时失败。
func TestInit_Order_DAOFirst(t *testing.T) {
	defer resetChatState()
	resetDAOState()

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	factory := &fakeChatModelFactory{cm: &fakeChatModel{}}

	err := InitWith(cfg, factory)
	if err == nil {
		t.Fatal("expected error when dao not initialized")
	}
	if !strings.Contains(err.Error(), "dao") {
		t.Fatalf("error should mention dao, got: %v", err)
	}
}

// TestInit_Success_WithDAO 验证 DAO 已初始化时 chat 可以成功。
func TestInit_Success_WithDAO(t *testing.T) {
	defer resetChatState()
	defer resetDAOState()

	cleanupDAO := initDAOForTest(t)
	defer cleanupDAO()

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	factory := &fakeChatModelFactory{cm: &fakeChatModel{}}

	err := InitWith(cfg, factory)
	if err != nil {
		t.Fatalf("InitWith() unexpected error: %v", err)
	}
	if !IsInitialized() {
		t.Fatal("should be initialized after successful InitWith()")
	}
	if GetChat() == nil {
		t.Fatal("GetChat() should return non-nil after successful init")
	}
	if factory.createCount != 1 {
		t.Fatalf("expected 1 factory call, got %d", factory.createCount)
	}
}

// --- 修复 4: Chat 初始化成功契约测试 ---

// TestInit_GormDBNil_Error 验证 DAO 已初始化但 GetGormDB 返回 nil 时 chat 返回明确错误。
func TestInit_GormDBNil_Error(t *testing.T) {
	defer resetChatState()
	defer resetDAOState()

	// 使用不设置 gormDB 的 fakeDatabase（GormDB() 返回 nil）
	fakeDB := &fakeDatabase{dsn: "test-dsn"} // gormDB 为 nil
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	if err := dao.InitDB(cfg, factory); err != nil {
		t.Fatalf("failed to init DAO: %v", err)
	}

	chatCfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	cmFactory := &fakeChatModelFactory{cm: &fakeChatModel{}}

	err := InitWith(chatCfg, cmFactory)
	if err == nil {
		t.Fatal("expected error when GetGormDB returns nil")
	}
	if !strings.Contains(err.Error(), "GetGormDB returned nil") {
		t.Fatalf("expected 'GetGormDB returned nil' error, got: %v", err)
	}
	// 工厂应被调用（因为 DAO 检查在工厂之后）
	// 实际上 DAO 检查在工厂之后，所以工厂应被调用
	if cmFactory.createCount != 1 {
		t.Fatalf("factory should be called before gormDB check, got %d calls", cmFactory.createCount)
	}
	if IsInitialized() {
		t.Fatal("should not be initialized when GetGormDB returns nil")
	}
}

// --- Stream context propagation and save contract tests ---

// streamingChatModel is a fake BaseChatModel for GetAnswerStream tests.
// It validates context propagation and supports blocking, cancellation, and error injection.
type streamingChatModel struct {
	mu            sync.Mutex
	receivedCtx   context.Context
	ctxReceived   chan struct{}
	blockRecv     chan struct{}
	chunks        []*schema.Message
	streamErr     error
	streamStarted chan struct{}
}

func newStreamingChatModel(chunks []string) *streamingChatModel {
	msgs := make([]*schema.Message, len(chunks))
	for i, c := range chunks {
		msgs[i] = &schema.Message{Role: schema.Assistant, Content: c}
	}
	return &streamingChatModel{
		chunks:        msgs,
		ctxReceived:   make(chan struct{}),
		blockRecv:     make(chan struct{}),
		streamStarted: make(chan struct{}),
	}
}

func (m *streamingChatModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: "assistant", Content: "test"}, nil
}

func (m *streamingChatModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.receivedCtx = ctx
	m.mu.Unlock()
	close(m.ctxReceived)

	if m.streamErr != nil {
		return nil, m.streamErr
	}

	sr, sw := schema.Pipe[*schema.Message](10)
	go func() {
		defer sw.Close()
		close(m.streamStarted)
		for _, chunk := range m.chunks {
			select {
			case <-ctx.Done():
				return
			case <-m.blockRecv:
				return
			default:
			}
			sw.Send(chunk, nil)
		}
		// If no chunks, block until context is cancelled or explicitly released.
		// This simulates a model that keeps the stream open (e.g., waiting for more data).
		if len(m.chunks) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-m.blockRecv:
				return
			}
		}
	}()
	return sr, nil
}

func (m *streamingChatModel) getReceivedCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.receivedCtx
}

// fakeMessageSaver records SaveMessage calls without a real database.
type fakeMessageSaver struct {
	mu         sync.Mutex
	savedMsgs  []*schema.Message
	saveErr    error
	saveCalled int
	convIDs    []string
	roles      []string
}

func (f *fakeMessageSaver) SaveMessage(mess *schema.Message, convID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveCalled++
	f.convIDs = append(f.convIDs, convID)
	f.roles = append(f.roles, string(mess.Role))
	if f.saveErr != nil {
		return f.saveErr
	}
	f.savedMsgs = append(f.savedMsgs, mess)
	return nil
}

func (f *fakeMessageSaver) GetHistory(convID string, limit int) ([]*schema.Message, error) {
	return []*schema.Message{}, nil
}

func (f *fakeMessageSaver) getSaveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saveCalled
}

func (f *fakeMessageSaver) getSavedContent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.savedMsgs) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, m := range f.savedMsgs {
		sb.WriteString(m.Content)
	}
	return sb.String()
}

// newTestChat creates a Chat instance with injected fake dependencies for testing.
// No global Init/DAO required.
func newTestChat(cm model.BaseChatModel, saver *fakeMessageSaver) *Chat {
	return &Chat{
		cm: cm,
		eh: saver,
	}
}

// drainStreamContent consumes all chunks from a StreamReader and returns the concatenated content.
// Does NOT close the reader; caller must invoke the cleanup function.
func drainStreamContent(t *testing.T, sr *schema.StreamReader[*schema.Message]) string {
	t.Helper()
	var sb strings.Builder
	for {
		chunk, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			return sb.String()
		}
		if err != nil {
			return sb.String()
		}
		if chunk != nil {
			sb.WriteString(chunk.Content)
		}
	}
}

// waitForSave polls until the saver has been called at least `count` times or timeout.
func waitForSave(t *testing.T, saver *fakeMessageSaver, count int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if saver.getSaveCount() >= count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for save (count=%d, want>=%d)", saver.getSaveCount(), count)
}

// callCleanup calls cleanup in a goroutine (since it blocks waiting for background).
// Returns a function that waits for cleanup to complete with a timeout.
func callCleanup(cleanup func(StreamResult), result StreamResult) func(time.Duration) bool {
	done := make(chan struct{})
	go func() {
		cleanup(result)
		close(done)
	}()
	return func(timeout time.Duration) bool {
		select {
		case <-done:
			return true
		case <-time.After(timeout):
			return false
		}
	}
}

// TestGetAnswerStream_ModelReceivesContext verifies the model's Stream method
// receives the caller's context (not context.Background()).
func TestGetAnswerStream_ModelReceivesContext(t *testing.T) {
	fm := newStreamingChatModel([]string{"hello"})
	saver := &fakeMessageSaver{}
	c := newTestChat(fm, saver)

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "test-value")

	sr, cleanup, err := c.GetAnswerStream(ctx, "conv-1", nil, "question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer cleanup(StreamResult{})

	// Wait for model to receive context
	select {
	case <-fm.ctxReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for model to receive context")
	}

	modelCtx := fm.getReceivedCtx()
	if modelCtx.Value(ctxKey{}) != "test-value" {
		t.Fatal("model did not receive the caller's context value")
	}

	// Drain the stream
	content := drainStreamContent(t, sr)
	if content != "hello" {
		t.Errorf("expected 'hello', got %q", content)
	}

	// Signal success so save goroutine can proceed
	cleanup(StreamResult{Success: true})
}

// TestGetAnswerStream_StreamCancelErrorChain verifies that when stream creation
// fails, the error chain is preserved with %w.
func TestGetAnswerStream_StreamCancelErrorChain(t *testing.T) {
	sentinel := errors.New("model stream sentinel")
	fm := &streamingChatModel{streamErr: sentinel}
	// Initialize channels to avoid nil channel issues
	fm.ctxReceived = make(chan struct{})
	fm.blockRecv = make(chan struct{})
	fm.streamStarted = make(chan struct{})
	saver := &fakeMessageSaver{}
	c := newTestChat(fm, saver)

	_, _, err := c.GetAnswerStream(context.Background(), "conv-1", nil, "question")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected errors.Is to recognize sentinel, got: %v", err)
	}
}

// TestGetAnswerStream_BlockingRecvCancel verifies that cancelling the context
// during a blocking Recv causes the stream to exit, and cleanup completes.
func TestGetAnswerStream_BlockingRecvCancel(t *testing.T) {
	fm := newStreamingChatModel(nil) // no chunks, model blocks waiting for ctx
	saver := &fakeMessageSaver{}
	c := newTestChat(fm, saver)

	ctx, cancel := context.WithCancel(context.Background())

	sr, cleanup, err := c.GetAnswerStream(ctx, "conv-1", nil, "question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wait for model to start
	select {
	case <-fm.streamStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stream to start")
	}

	// Cancel context — model observes cancel, pipe closes, background Recv exits
	cancel()

	// The stream reader should return an error or EOF (context cancelled propagated through model)
	_, recvErr := sr.Recv()
	if recvErr == nil {
		t.Fatal("expected error or EOF after cancel")
	}
	if !errors.Is(recvErr, context.Canceled) && !errors.Is(recvErr, io.EOF) {
		t.Logf("recv error after cancel: %v (acceptable)", recvErr)
	}

	// cleanup must complete (background exits)
	waitFn := callCleanup(cleanup, StreamResult{})
	if !waitFn(5 * time.Second) {
		t.Fatal("timeout waiting for cleanup after cancel")
	}

	// No assistant save
	if saver.getSaveCount() > 1 {
		t.Errorf("expected at most 1 save on cancel, got %d", saver.getSaveCount())
	}
}

// TestGetAnswerStream_NormalEOF_SaveOnce verifies that a normal EOF saves
// the complete answer exactly once. cleanup blocks until background completes.
func TestGetAnswerStream_NormalEOF_SaveOnce(t *testing.T) {
	fm := newStreamingChatModel([]string{"hello", " world"})
	saver := &fakeMessageSaver{}
	c := newTestChat(fm, saver)

	sr, cleanup, err := c.GetAnswerStream(context.Background(), "conv-1", nil, "question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := drainStreamContent(t, sr)
	if content != "hello world" {
		t.Errorf("expected 'hello world', got: %q", content)
	}

	// cleanup blocks until background completes (save has happened or not by return)
	waitFn := callCleanup(cleanup, StreamResult{Success: true})
	if !waitFn(5 * time.Second) {
		t.Fatal("timeout waiting for cleanup")
	}

	// docsMessages saves user question (1), background saves assistant answer (2)
	if saver.getSaveCount() != 2 {
		t.Errorf("expected 2 saves (user + assistant), got %d", saver.getSaveCount())
	}
	fullContent := saver.getSavedContent()
	if !strings.Contains(fullContent, "hello world") {
		t.Errorf("saved content should contain 'hello world', got: %q", fullContent)
	}
	if !strings.Contains(fullContent, "question") {
		t.Errorf("saved content should contain 'question' (user message), got: %q", fullContent)
	}
}

// partialErrorChatModel sends one chunk then a non-EOF error.
type partialErrorChatModel struct {
	mu          sync.Mutex
	receivedCtx context.Context
	ctxReceived chan struct{}
}

func (m *partialErrorChatModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: "assistant", Content: "test"}, nil
}

func (m *partialErrorChatModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.receivedCtx = ctx
	m.mu.Unlock()
	close(m.ctxReceived)

	sr, sw := schema.Pipe[*schema.Message](10)
	go func() {
		defer sw.Close()
		sw.Send(&schema.Message{Role: schema.Assistant, Content: "partial"}, nil)
		sw.Send(nil, fmt.Errorf("model error mid-stream"))
	}()
	return sr, nil
}

// TestGetAnswerStream_PartialThenError_NoSave verifies that partial content
// followed by a model error does NOT save the partial answer.
func TestGetAnswerStream_PartialThenError_NoSave(t *testing.T) {
	fm := &partialErrorChatModel{ctxReceived: make(chan struct{})}
	saver := &fakeMessageSaver{}
	c := newTestChat(fm, saver)

	sr, cleanup, err := c.GetAnswerStream(context.Background(), "conv-1", nil, "question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Consume stream: should get "partial" then error
	var gotContent string
	for {
		chunk, recvErr := sr.Recv()
		if recvErr != nil {
			break
		}
		if chunk != nil {
			gotContent += chunk.Content
		}
	}

	if gotContent != "partial" {
		t.Errorf("expected 'partial' content, got: %q", gotContent)
	}

	// cleanup blocks until background exits; Success=false → no save
	waitFn := callCleanup(cleanup, StreamResult{})
	if !waitFn(5 * time.Second) {
		t.Fatal("timeout waiting for cleanup")
	}

	// docsMessages saves user question (1 save), but the stream goroutine
	// should NOT save the assistant answer (non-EOF error → Success=false).
	if saver.getSaveCount() != 1 {
		t.Errorf("expected 1 save (only user question from docsMessages) on error, got %d", saver.getSaveCount())
	}
	if strings.Contains(saver.getSavedContent(), "partial") {
		t.Error("saved content should NOT contain partial assistant answer")
	}
}

// TestGetAnswerStream_Cancel_NoSave verifies that cancelling the context
// does NOT save a partial answer. cleanup blocks until background exits.
func TestGetAnswerStream_Cancel_NoSave(t *testing.T) {
	fm := newStreamingChatModel(nil)
	saver := &fakeMessageSaver{}
	c := newTestChat(fm, saver)

	ctx, cancel := context.WithCancel(context.Background())

	_, cleanup, err := c.GetAnswerStream(ctx, "conv-1", nil, "question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wait for model to start
	select {
	case <-fm.streamStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stream to start")
	}

	// Cancel context
	cancel()

	// cleanup blocks until background exits
	waitFn := callCleanup(cleanup, StreamResult{})
	if !waitFn(5 * time.Second) {
		t.Fatal("timeout waiting for cleanup")
	}

	// docsMessages saves user question (1 save), cancel should prevent
	// the assistant answer from being saved.
	if saver.getSaveCount() > 1 {
		t.Errorf("expected at most 1 save (user question) on cancel, got %d", saver.getSaveCount())
	}
	if strings.Contains(saver.getSavedContent(), "chunk") {
		t.Error("saved content should NOT contain assistant answer on cancel")
	}
}

// TestGetAnswerStream_WriteFailure_ModelObservesCancel_NoSave verifies that SSE write failure
// with cancel causes the model to observe cancellation and no save occurs.
func TestGetAnswerStream_WriteFailure_ModelObservesCancel_NoSave(t *testing.T) {
	fm := newStreamingChatModel(nil) // blocks on ctx.Done()
	saver := &fakeMessageSaver{}
	c := newTestChat(fm, saver)

	ctx, cancel := context.WithCancel(context.Background())

	_, cleanup, err := c.GetAnswerStream(ctx, "conv-1", nil, "question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wait for model to start
	select {
	case <-fm.streamStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stream to start")
	}

	// Simulate write failure: cancel context (controller would cancel on write failure)
	cancel()

	// cleanup with failure; blocks until background exits
	waitFn := callCleanup(cleanup, StreamResult{})
	if !waitFn(5 * time.Second) {
		t.Fatal("timeout waiting for cleanup")
	}

	// Model should have observed context cancellation
	modelCtx := fm.getReceivedCtx()
	if modelCtx.Err() == nil {
		t.Error("model context should be cancelled")
	}

	// docsMessages saves user question (1), no assistant save
	if saver.getSaveCount() > 1 {
		t.Errorf("expected at most 1 save (user question) after cancel, got %d", saver.getSaveCount())
	}
}

// TestGetAnswerStream_CancelThenEOF_NoDONE verifies at the chat level that
// cancellation followed by EOF does not result in a save.
func TestGetAnswerStream_CancelThenEOF_NoDONE(t *testing.T) {
	fm := newStreamingChatModel([]string{"data"})
	saver := &fakeMessageSaver{}
	c := newTestChat(fm, saver)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before streaming

	sr, cleanup, err := c.GetAnswerStream(ctx, "conv-1", nil, "question")
	if err != nil {
		return // stream creation may fail due to context
	}

	// Drain foreground
	drainStreamContent(t, sr)

	// cleanup blocks until background exits
	waitFn := callCleanup(cleanup, StreamResult{})
	if !waitFn(5 * time.Second) {
		t.Fatal("timeout waiting for cleanup")
	}

	if strings.Contains(saver.getSavedContent(), "data") {
		t.Error("saved content should NOT contain assistant answer when context cancelled")
	}
}

// TestGetAnswerStream_ConcurrentSessions_Isolation verifies that multiple
// concurrent streaming sessions do not interfere with each other.
func TestGetAnswerStream_ConcurrentSessions_Isolation(t *testing.T) {
	const n = 3
	fms := make([]*streamingChatModel, n)
	savers := make([]*fakeMessageSaver, n)
	chats := make([]*Chat, n)
	srs := make([]*schema.StreamReader[*schema.Message], n)
	cleanups := make([]func(StreamResult), n)

	for i := 0; i < n; i++ {
		fms[i] = newStreamingChatModel([]string{fmt.Sprintf("session%d-", i), "answer"})
		savers[i] = &fakeMessageSaver{}
		chats[i] = newTestChat(fms[i], savers[i])
	}

	// Start all streams concurrently
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sr, cl, err := chats[idx].GetAnswerStream(context.Background(), fmt.Sprintf("conv-%d", idx), nil, "q")
			if err != nil {
				t.Errorf("session %d: unexpected error: %v", idx, err)
				return
			}
			srs[idx] = sr
			cleanups[idx] = cl
		}(i)
	}
	wg.Wait()

	// Consume each stream and verify isolation
	for i := 0; i < n; i++ {
		if srs[i] == nil {
			t.Fatalf("session %d: nil stream reader", i)
		}
		content := drainStreamContent(t, srs[i])
		expected := fmt.Sprintf("session%d-answer", i)
		if content != expected {
			t.Errorf("session %d: expected %q, got %q", i, expected, content)
		}
	}

	// Signal success for all sessions (cleanup blocks until background completes)
	for i := 0; i < n; i++ {
		if cleanups[i] != nil {
			cleanups[i](StreamResult{Success: true})
		}
	}

	// After cleanup returns, save has already happened (or not)
	// Verify each saver got the correct content
	for i := 0; i < n; i++ {
		expectedAnswer := fmt.Sprintf("session%d-answer", i)
		fullContent := savers[i].getSavedContent()
		if !strings.Contains(fullContent, expectedAnswer) {
			t.Errorf("session %d: saved content should contain %q, got %q", i, expectedAnswer, fullContent)
		}
		if savers[i].getSaveCount() != 2 {
			t.Errorf("session %d: expected 2 saves (user + assistant), got %d", i, savers[i].getSaveCount())
		}
	}
}

// TestGetAnswerStream_BackgroundEOFThenForegroundFail_NoSave verifies that when
// background reads EOF but foreground then fails, no save occurs.
func TestGetAnswerStream_BackgroundEOFThenForegroundFail_NoSave(t *testing.T) {
	fm := newStreamingChatModel([]string{"answer"})
	saver := &fakeMessageSaver{}
	c := newTestChat(fm, saver)

	sr, cleanup, err := c.GetAnswerStream(context.Background(), "conv-1", nil, "question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Drain foreground (gets "answer" then EOF)
	content := drainStreamContent(t, sr)
	if content != "answer" {
		t.Errorf("expected 'answer', got %q", content)
	}

	// Foreground reports failure (simulating SSE write error after EOF)
	// cleanup blocks until background exits
	waitFn := callCleanup(cleanup, StreamResult{}) // Success=false
	if !waitFn(5 * time.Second) {
		t.Fatal("timeout waiting for cleanup")
	}

	// Background saw cleanEOF, but foreground Success=false → no save
	if saver.getSaveCount() != 1 {
		t.Errorf("expected 1 save (user question only), got %d", saver.getSaveCount())
	}
	if strings.Contains(saver.getSavedContent(), "answer") {
		t.Error("should NOT save assistant answer when foreground failed")
	}
}

// TestGetAnswerStream_CleanupIdempotent verifies that cleanup can be called
// multiple times safely; only the first call decides the result.
func TestGetAnswerStream_CleanupIdempotent(t *testing.T) {
	fm := newStreamingChatModel([]string{"hello"})
	saver := &fakeMessageSaver{}
	c := newTestChat(fm, saver)

	sr, cleanup, err := c.GetAnswerStream(context.Background(), "conv-1", nil, "question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Drain the stream
	drainStreamContent(t, sr)

	// First call: Success=true (decides the result)
	waitFn := callCleanup(cleanup, StreamResult{Success: true})
	if !waitFn(5 * time.Second) {
		t.Fatal("timeout waiting for first cleanup")
	}

	// Second call: should not block or panic (sync.Once)
	waitFn2 := callCleanup(cleanup, StreamResult{}) // different result, should be ignored
	if !waitFn2(5 * time.Second) {
		t.Fatal("timeout waiting for second cleanup")
	}

	// Third call: same
	waitFn3 := callCleanup(cleanup, StreamResult{Success: true})
	if !waitFn3(5 * time.Second) {
		t.Fatal("timeout waiting for third cleanup")
	}

	// First call decided Success=true, so save should have happened
	if saver.getSaveCount() != 2 {
		t.Errorf("expected 2 saves (user + assistant), got %d", saver.getSaveCount())
	}
	if !strings.Contains(saver.getSavedContent(), "hello") {
		t.Error("saved content should contain 'hello'")
	}
}

// TestGetAnswerStream_DirectFailCleanup_NoManualCancel verifies that cleanup
// with Success=false works without pre-cancelling the context. The model observes
// cancellation via the derived context released in cleanup, and no save occurs.
func TestGetAnswerStream_DirectFailCleanup_NoManualCancel(t *testing.T) {
	fm := newStreamingChatModel(nil) // blocks on ctx.Done()
	saver := &fakeMessageSaver{}
	c := newTestChat(fm, saver)

	// No manual cancel — cleanup itself triggers cancel via derived context
	sr, cleanup, err := c.GetAnswerStream(context.Background(), "conv-1", nil, "question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wait for model to start
	select {
	case <-fm.streamStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stream to start")
	}

	// Drain the stream (no chunks, blocks until model pipe closes)
	// Don't manually close sr — cleanup will close srs[0] which propagates to the pipe
	// Instead, call cleanup in a goroutine and verify it completes
	waitFn := callCleanup(cleanup, StreamResult{})
	if !waitFn(5 * time.Second) {
		t.Fatal("timeout waiting for cleanup")
	}

	// Model should observe cancellation (derived context released by cleanup)
	modelCtx := fm.getReceivedCtx()
	if modelCtx.Err() == nil {
		t.Error("model context should be cancelled after cleanup")
	}

	// docsMessages saves user question (1), no assistant save
	if saver.getSaveCount() > 1 {
		t.Errorf("expected at most 1 save (user question), got %d", saver.getSaveCount())
	}

	// Suppress unused variable warning
	_ = sr
}

// TestChatStream_BackgroundDoneThenForegroundFail_NoSave verifies that when
// background has consumed all chunks (cleanEOF) but foreground reports failure,
// no save occurs. Uses signal-based ordering.
func TestChatStream_BackgroundDoneThenForegroundFail_NoSave(t *testing.T) {
	fm := newStreamingChatModel([]string{"answer"})
	saver := &fakeMessageSaver{}
	c := newTestChat(fm, saver)

	sr, cleanup, err := c.GetAnswerStream(context.Background(), "conv-1", nil, "question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Drain foreground (gets "answer" then EOF)
	content := drainStreamContent(t, sr)
	if content != "answer" {
		t.Errorf("expected 'answer', got %q", content)
	}

	// Background should have seen cleanEOF by now (model sent all chunks and closed)
	// Give background a moment to process EOF
	time.Sleep(50 * time.Millisecond)

	// Foreground reports failure (simulating SSE write error after background EOF)
	waitFn := callCleanup(cleanup, StreamResult{}) // Success=false
	if !waitFn(5 * time.Second) {
		t.Fatal("timeout waiting for cleanup")
	}

	// Background saw cleanEOF, but foreground Success=false → no save
	if saver.getSaveCount() != 1 {
		t.Errorf("expected 1 save (user question only), got %d", saver.getSaveCount())
	}
	if strings.Contains(saver.getSavedContent(), "answer") {
		t.Error("should NOT save assistant answer when foreground failed")
	}
}

// TestChatStream_ConcurrentConvID_SaverRecords verifies that concurrent streams
// on the same Chat with different convIDs record correct role, convID, and content.
func TestChatStream_ConcurrentConvID_SaverRecords(t *testing.T) {
	const n = 3
	// Shared saver across all sessions (same Chat instance)
	sharedSaver := &fakeMessageSaver{}

	// Each session needs its own model (streamingChatModel can't be reused)
	type sessionModel struct {
		fm *streamingChatModel
		sr *schema.StreamReader[*schema.Message]
		cl func(StreamResult)
	}

	models := make([]*streamingChatModel, n)
	for i := 0; i < n; i++ {
		models[i] = newStreamingChatModel([]string{"shared-answer"})
	}

	// Create a Chat that routes to different models based on convID
	c := &Chat{
		cm: &routingChatModel{models: models},
		eh: sharedSaver,
	}

	var wg sync.WaitGroup
	srs := make([]*schema.StreamReader[*schema.Message], n)
	cleanups := make([]func(StreamResult), n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sr, cl, err := c.GetAnswerStream(context.Background(), fmt.Sprintf("conv-%d", idx), nil, fmt.Sprintf("q%d", idx))
			if err != nil {
				t.Errorf("session %d: unexpected error: %v", idx, err)
				return
			}
			srs[idx] = sr
			cleanups[idx] = cl
		}(i)
	}
	wg.Wait()

	// Drain all streams
	for i := 0; i < n; i++ {
		if srs[i] == nil {
			t.Fatalf("session %d: nil stream reader", i)
		}
		content := drainStreamContent(t, srs[i])
		if content != "shared-answer" {
			t.Errorf("session %d: expected 'shared-answer', got %q", i, content)
		}
	}

	// Cleanup all with success
	for i := 0; i < n; i++ {
		if cleanups[i] != nil {
			cleanups[i](StreamResult{Success: true})
		}
	}

	// Each session: user question (1) + assistant answer (1) = 2 saves per session
	expectedSaves := n * 2
	if sharedSaver.getSaveCount() != expectedSaves {
		t.Errorf("expected %d saves, got %d", expectedSaves, sharedSaver.getSaveCount())
	}

	// Verify convIDs and roles
	sharedSaver.mu.Lock()
	defer sharedSaver.mu.Unlock()
	convCount := map[string]int{}
	for i, cid := range sharedSaver.convIDs {
		convCount[cid]++
		role := sharedSaver.roles[i]
		if role != string(schema.User) && role != string(schema.Assistant) {
			t.Errorf("unexpected role %q for convID %s", role, cid)
		}
	}
	for i := 0; i < n; i++ {
		expectedCID := fmt.Sprintf("conv-%d", i)
		if convCount[expectedCID] != 2 {
			t.Errorf("expected 2 saves for %s, got %d", expectedCID, convCount[expectedCID])
		}
	}

	// Verify assistant role appears exactly once per convID
	assistantPerConv := map[string]int{}
	for i, cid := range sharedSaver.convIDs {
		if sharedSaver.roles[i] == string(schema.Assistant) {
			assistantPerConv[cid]++
		}
	}
	for i := 0; i < n; i++ {
		expectedCID := fmt.Sprintf("conv-%d", i)
		if assistantPerConv[expectedCID] != 1 {
			t.Errorf("expected exactly 1 assistant save for %s, got %d", expectedCID, assistantPerConv[expectedCID])
		}
	}
}

// routingChatModel routes Stream calls to different models based on call order.
// Thread-safe for concurrent use.
type routingChatModel struct {
	mu     sync.Mutex
	idx    int
	models []*streamingChatModel
}

func (r *routingChatModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: "assistant", Content: "test"}, nil
}

func (r *routingChatModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	r.mu.Lock()
	idx := r.idx
	r.idx++
	r.mu.Unlock()
	if idx >= len(r.models) {
		return nil, errors.New("no more models")
	}
	return r.models[idx].Stream(ctx, in, opts...)
}

// TestChatStream_ModelReceivesDeadlineAndValue verifies that a real
// context.WithDeadline propagates both deadline and values to the model
// through the derived context chain.
func TestChatStream_ModelReceivesDeadlineAndValue(t *testing.T) {
	fm := newStreamingChatModel([]string{"hello"})
	saver := &fakeMessageSaver{}
	c := newTestChat(fm, saver)

	type ctxKey struct{}
	deadline := time.Now().Add(30 * time.Second)
	parentCtx, parentCancel := context.WithDeadline(context.Background(), deadline)
	defer parentCancel()
	parentCtx = context.WithValue(parentCtx, ctxKey{}, "test-value")

	sr, cleanup, err := c.GetAnswerStream(parentCtx, "conv-1", nil, "question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer cleanup(StreamResult{})

	// Wait for model to receive context
	select {
	case <-fm.ctxReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for model to receive context")
	}

	modelCtx := fm.getReceivedCtx()

	// Check value propagation
	if modelCtx.Value(ctxKey{}) != "test-value" {
		t.Fatal("model did not receive the caller's context value")
	}

	// Check deadline propagation
	modelDeadline, ok := modelCtx.Deadline()
	if !ok {
		t.Fatal("model context should have a deadline")
	}
	if !modelDeadline.Equal(deadline) {
		t.Errorf("model deadline %v != parent deadline %v", modelDeadline, deadline)
	}

	// Drain the stream
	content := drainStreamContent(t, sr)
	if content != "hello" {
		t.Errorf("expected 'hello', got %q", content)
	}

	cleanup(StreamResult{Success: true})
}
