package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/gogf/gf/v2/frame/g"
	v1 "github.com/wangle201210/go-rag/server/api/rag/v1"
	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/grader"
	"github.com/wangle201210/go-rag/server/core/indexer"
	"github.com/wangle201210/go-rag/server/core/retriever"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
	"github.com/wangle201210/go-rag/server/core/vector"
	"github.com/wangle201210/go-rag/server/internal/logic/knowledge"
	"github.com/wangle201210/go-rag/server/internal/model/entity"
)

var (
	// ErrBackgroundFull 后台任务队列已满
	ErrBackgroundFull = errors.New("background task queue full")
	// ErrShuttingDown Rag 已关闭，拒绝新请求
	ErrShuttingDown = errors.New("rag is shutting down")
)

const (
	scoreThreshold = 1.05 // 设置一个很小的阈值
	esTopK         = 50
	esTryFindDoc   = 10
)

// docSearcher 文档检索边界：按 ID 集合从向量库/ES 取回文档，支持测试注入。
type docSearcher interface {
	SearchDocumentsByIDs(ctx context.Context, knowledgeName string, docIDs []string, size int) ([]*schema.Document, error)
}

type Rag struct {
	syncIndexer  syncIndexer                                    // 注入边界：同步索引
	asyncIndexer asyncIndexer                                   // 注入边界：异步索引
	idxerAsync   compose.Runnable[[]*schema.Document, []string] // 异步文档索引器
	rtrvr        compose.Runnable[string, []*schema.Document]
	qaRtrvr      compose.Runnable[string, []*schema.Document]
	client       *elasticsearch.Client // 保留用于兼容
	cm           model.BaseChatModel

	grader *grader.Grader // 暂时先弃用，使用 grader 会严重影响rag的速度
	conf   *config.Config

	docRet    docRetriever              // 注入边界：用于测试替换
	rwModel   model.BaseChatModel       // 注入边界：用于测试替换 rewrite model
	asyncWait func(ctx context.Context) // 可注入等待机制，测试用

	// 以下三个字段与 syncIndexer/asyncIndexer/docRet 同模式：
	// 构建时固定、运行期只读，不使用可变包级函数替换。
	docSearch       docSearcher                                                                   // 注入边界：文档检索
	saveChunksFn    func(ctx context.Context, docID int64, chunks []entity.KnowledgeChunks) error // 注入边界：保存 chunks
	updateDocStatus func(ctx context.Context, docID int64, status int) error                      // 注入边界：更新文档状态

	// 后台任务生命周期管理（Stage 1D-3-A）
	mu            sync.Mutex
	closed        bool
	bgWG          sync.WaitGroup
	bgSem         chan struct{}
	bgRootCtx     context.Context
	bgRootCancel  context.CancelFunc
	shutdownOnce  sync.Once
	done          chan struct{}
	taskTimeout   time.Duration
	maxBackground int

	// 统一终结处理：后台失败时标记文档状态（Stage 1D-3-B）
	markFailed func(ctx context.Context, docID int64, err error)

	// Stage 2C-2: Vector store reference for fingerprint operations (C5: no cache)
	vectorStore vector.VectorStore // Vector store for fingerprint operations

	// D4: 启动时固定的物理资源 ID，运行期只读。
	// VerifyFingerprint 成功时设置；checkFingerprint 每次比对。
	// 如果 resourceID 变化，说明物理资源已被替换。
	boundResourceID string
}

func New(ctx context.Context, conf *config.Config) (rag *Rag, err error) {
	dimensions, err := conf.ResolveDimensions()
	if err != nil {
		return nil, err
	}
	conf = conf.Copy()
	conf.EmbeddingDimensions = dimensions
	// 2C-3: Active Provider embedding path no longer blocked here.
	// Guard moved to common.NewEmbedding which goes through conf.Registry.GetEmbedder.
	// Identity/dimensions/binding validation happens at construction time, not at entry.
	if len(conf.IndexName) == 0 {
		return nil, fmt.Errorf("indexName is empty")
	}
	if err := vector.ValidatePhysicalName(conf.IndexName); err != nil {
		return nil, err
	}
	if conf.QdrantClient != nil {
		return nil, vector.ErrResourceBindingNotReady
	}
	if !conf.ManagedResourceLifecycle {
		return nil, vector.ErrManagedLifecycleRequired
	}
	if conf.VectorIOGuard == nil {
		return nil, vector.ErrResourceBindingNotReady
	}

	// Fix 4: 不在此处临时设置全局 Registry。内部需要 Registry 时通过 conf.Registry 显式传入。
	// 成功后由调用方（defaultRagFactory）发布全局 Registry；失败不影响全局状态。

	// C1: Index creation is now handled by the factory (defaultRagFactory),
	// which calls vs.CreateIndex(ctx, indexName, fp) with atomic fingerprint.
	// core.New() no longer creates indexes directly.
	buildIndex, err := indexer.BuildIndexer(ctx, conf)
	if err != nil {
		return nil, err
	}

	// QA 配置：条件预热 qaModel
	qaEnabled := conf.QAEnabled == nil || *conf.QAEnabled
	var qaFactory func(context.Context) (model.BaseChatModel, error)
	if qaEnabled {
		// Fix 4: 使用显式传入的 Registry
		qaModel, err := common.GetQAModelWithRegistry(ctx, nil, conf.Registry)
		if err != nil {
			return nil, fmt.Errorf("GetQAModel failed: %w", err)
		}
		_ = qaModel // 预热完成
		qaFactory = func(ctx context.Context) (model.BaseChatModel, error) {
			return common.GetQAModelWithRegistry(ctx, nil, conf.Registry)
		}
	}

	buildIndexAsync, err := indexer.BuildIndexerAsync(ctx, conf, qaFactory)
	if err != nil {
		return nil, err
	}
	buildRetriever, err := retriever.BuildRetriever(ctx, conf)
	if err != nil {
		return nil, err
	}
	qaCtx := context.WithValue(ctx, coretypes.RetrieverFieldKey, coretypes.FieldQAContentVector)
	qaRetriever, err := retriever.BuildRetriever(qaCtx, conf)
	if err != nil {
		return nil, err
	}
	// Fix 4: 使用显式传入的 Registry
	cm, err := common.GetChatModelWithRegistry(ctx, nil, conf.Registry)
	if err != nil {
		g.Log().Error(ctx, "GetChatModel failed, err=%v", err)
		return nil, err
	}

	// 后台任务生命周期配置钳位
	maxBG := conf.MaxBackgroundTasks
	if maxBG <= 0 {
		maxBG = 8
	}
	if maxBG > 256 {
		maxBG = 256
	}
	timeout := conf.BackgroundTaskTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if timeout > 60*time.Minute {
		timeout = 60 * time.Minute
	}

	bgRootCtx, bgRootCancel := context.WithCancel(context.Background())

	rag = &Rag{
		idxerAsync: buildIndexAsync,
		rtrvr:      buildRetriever,
		qaRtrvr:    qaRetriever,
		client:     conf.Client,
		cm:         cm,
		conf:       conf,
		// grader:  grader.NewGrader(cm),

		// 后台任务生命周期
		bgRootCtx:     bgRootCtx,
		bgRootCancel:  bgRootCancel,
		bgSem:         make(chan struct{}, maxBG),
		done:          make(chan struct{}),
		taskTimeout:   timeout,
		maxBackground: maxBG,
	}
	rag.syncIndexer = &prodSyncIndexer{runnable: buildIndex} // 发布前设置注入字段，运行期间只读
	rag.asyncIndexer = &prodAsyncIndexer{rag: rag}           // 发布前设置注入字段，运行期间只读
	rag.docRet = &prodDocRetriever{rag: rag}                 // 发布前设置注入字段，运行期间只读
	rag.docSearch = conf                                     // *config.Config 实现 SearchDocumentsByIDs
	rag.saveChunksFn = knowledge.SaveChunksDataIndexingOnly
	rag.updateDocStatus = knowledge.UpdateDocumentsStatus

	// 默认 markFailed 实现：调用 updateDocStatus 标记 Failed
	rag.markFailed = func(ctx context.Context, docID int64, err error) {
		if rag.updateDocStatus != nil {
			if statusErr := rag.updateDocStatus(ctx, docID, int(v1.StatusFailed)); statusErr != nil {
				g.Log().Errorf(ctx, "markFailed updateDocStatus failed: %v, original err: %v", statusErr, err)
			}
		}
	}

	return rag, nil
}

// VerifyFingerprint performs fingerprint verification.
// C5: Reads from storage every time (no caching).
// C4: ReadFingerprint now includes schema validation (dimensions check).
func (x *Rag) VerifyFingerprint(ctx context.Context, vs vector.VectorStore) error {
	if vs == nil {
		return fmt.Errorf("nil VectorStore")
	}
	if _, qdrant := vs.(*vector.QdrantVectorStore); qdrant {
		return vector.ErrResourceBindingNotReady
	}
	if _, es := vs.(*vector.ESVectorStore); es && (!x.conf.ManagedResourceLifecycle || x.conf.VectorIOGuard == nil) {
		return vector.ErrManagedLifecycleRequired
	}
	x.vectorStore = vs

	// Compute expected fingerprint from config
	expectedFP, err := x.computeExpectedFingerprint()
	if err != nil {
		return fmt.Errorf("compute expected fingerprint: %w", err)
	}

	// Read and verify from storage (C4: ReadFingerprint includes schema validation)
	storedFP, resourceID, err := vs.ReadFingerprint(ctx, x.conf.IndexName)
	if err != nil {
		if errors.Is(err, vector.ErrFingerprintMissing) {
			g.Log().Errorf(ctx,
				"Index %q has no fingerprint. "+
					"Refusing to auto-backfill. "+
					"Rebuild the index with current embedding config to establish fingerprint identity.",
				x.conf.IndexName)
			return fmt.Errorf("index %q has no fingerprint: %w", x.conf.IndexName, vector.ErrFingerprintMissing)
		}
		if errors.Is(err, vector.ErrFingerprintCorrupted) {
			g.Log().Errorf(ctx,
				"Index %q has corrupted fingerprint. "+
					"Refusing to proceed. "+
					"Rebuild the index with current embedding config.",
				x.conf.IndexName)
			return fmt.Errorf("index %q has corrupted fingerprint: %w", x.conf.IndexName, vector.ErrFingerprintCorrupted)
		}
		return fmt.Errorf("read fingerprint: %w", err)
	}

	// C5: Empty resourceID = identity incomplete, fail closed
	if resourceID == "" {
		return fmt.Errorf("index %q: %w (resourceID empty)", x.conf.IndexName, vector.ErrFingerprintCorrupted)
	}

	// Verify fingerprint matches expected
	if verifyErr := vector.VerifyFingerprint(storedFP, expectedFP); verifyErr != nil {
		return fmt.Errorf("fingerprint verification failed for index %q: %w", x.conf.IndexName, verifyErr)
	}

	g.Log().Infof(ctx, "Fingerprint verified for index %q (resourceID=%s)", x.conf.IndexName, resourceID)

	// D4: 固定物理资源 ID（启动时设置，运行期比对）
	x.boundResourceID = resourceID

	return nil
}

// computeExpectedFingerprint builds the expected fingerprint from current config.
func (x *Rag) computeExpectedFingerprint() (*vector.Fingerprint, error) {
	return ComputeFingerprintMeta(x.conf)
}

// ComputeFingerprintMeta builds a Fingerprint from config for identity binding.
// Standalone function usable before Rag creation (called from factory).
// 2C-3: Uses ResolveEmbeddingIdentity for single-source identity.
func ComputeFingerprintMeta(conf *config.Config) (*vector.Fingerprint, error) {
	providerName, modelID, dims, err := common.ResolveEmbeddingIdentity(conf)
	if err != nil {
		return nil, err
	}
	if modelID == "" {
		return nil, fmt.Errorf("embedding model not configured")
	}
	if dims <= 0 {
		return nil, fmt.Errorf("invalid embedding dimensions: %d", dims)
	}
	return vector.NewFingerprint(
		vector.FingerprintVersion,
		modelID,
		dims,
		providerName,
		"cosine",
	)
}

// GetFingerprint returns the cached fingerprint for the current index.
// C5: Cache removed; always returns nil. Callers should use checkFingerprint for verification.
func (x *Rag) GetFingerprint() *vector.Fingerprint {
	return nil
}

// checkFingerprint validates the fingerprint before embedding operations.
// C5: Reads from storage every time (no caching).
// Returns error if fingerprint is missing, invalid, or cannot be verified.
// D4: Compares resourceID with boundResourceID to detect physical resource replacement.
// E3: This check happens at the entry point; see verifyBoundResource for post-write verification.
func (x *Rag) checkFingerprint(ctx context.Context) error {
	// Ensure vector store is initialized
	if x.vectorStore == nil {
		return fmt.Errorf("vector store not set: VerifyFingerprint must be called after New()")
	}

	// C5: Read from vector store every time (no caching)
	storedFP, storedResourceID, err := x.vectorStore.ReadFingerprint(ctx, x.conf.IndexName)
	if err != nil {
		return fmt.Errorf("read fingerprint from vector store: %w", err)
	}

	// C5: Empty stored resourceID = identity incomplete, fail closed
	if storedResourceID == "" {
		return fmt.Errorf("index %q: %w (stored resourceID empty)", x.conf.IndexName, vector.ErrFingerprintCorrupted)
	}

	// Compute expected fingerprint from config
	expectedFP, err := x.computeExpectedFingerprint()
	if err != nil {
		return fmt.Errorf("compute expected fingerprint: %w", err)
	}

	// Verify match
	if verifyErr := vector.VerifyFingerprint(storedFP, expectedFP); verifyErr != nil {
		return fmt.Errorf("fingerprint verification failed for index %q: %w", x.conf.IndexName, verifyErr)
	}

	// D4: 比对 resourceID 与启动时固定的值（检测物理资源替换）
	if x.boundResourceID != "" && storedResourceID != x.boundResourceID {
		return fmt.Errorf("index %q: %w (bound=%s, current=%s)",
			x.conf.IndexName, vector.ErrResourceReplaced, x.boundResourceID, storedResourceID)
	}

	return nil
}

// verifyBoundResource checks that the current physical resource matches the bound one.
// E3: Call after actual Store/query operations to detect resource replacement during operation.
// This is a post-write verification — it cannot prevent writes to a replaced resource,
// but it detects the condition immediately after the operation completes.
//
// TOCTOU limitation: Between checkFingerprint (pre-entry) and verifyBoundResource (post-write),
// there is a window where the physical resource could be replaced (e.g., delete + recreate with
// same logical name but different UUID). Full atomic binding requires Option C (immutable physical
// names) which is pending separate approval.
func (x *Rag) verifyBoundResource(ctx context.Context) error {
	if x.vectorStore == nil {
		return fmt.Errorf("vector store not set: VerifyFingerprint must be called after New()")
	}
	// E3: If boundResourceID is empty, VerifyFingerprint was not called (e.g., test setup).
	// Skip verification to maintain backward compatibility with existing tests.
	if x.boundResourceID == "" {
		return nil
	}
	_, resourceID, err := x.vectorStore.ReadFingerprint(ctx, x.conf.IndexName)
	if err != nil {
		return fmt.Errorf("resource verification failed: %w", err)
	}
	if resourceID == "" {
		return fmt.Errorf("index %q: %w (resourceID empty)", x.conf.IndexName, vector.ErrFingerprintCorrupted)
	}
	if resourceID != x.boundResourceID {
		return fmt.Errorf("index %q: %w (bound=%s, current=%s)",
			x.conf.IndexName, vector.ErrResourceReplaced, x.boundResourceID, resourceID)
	}
	return nil
}

// GetKnowledgeBaseList 获取知识库列表
func (x *Rag) GetKnowledgeBaseList(ctx context.Context) (list []string, err error) {
	return x.conf.GetKnowledgeBaseList(ctx)
}

// Shutdown 停止接收新请求并等待已准入任务完成。
// ctx 超时后返回 ctx.Err()，但后台仍继续等待 bgWG，done 最终会 close。
// 后续调用 Shutdown 仍能等待同一 done channel。
func (x *Rag) Shutdown(ctx context.Context) error {
	x.shutdownOnce.Do(func() {
		x.mu.Lock()
		x.closed = true
		x.mu.Unlock()
		x.bgRootCancel()
		go func() {
			x.bgWG.Wait()
			close(x.done)
		}()
	})

	select {
	case <-x.done:
		return nil
	case <-ctx.Done():
		// 超时后后台 goroutine 仍继续等待 bgWG.Wait()，done 最终会 close
		// 本次返回 ctx.Err()，但下次调用 Shutdown 仍能等待同一 done
		return ctx.Err()
	}
}
