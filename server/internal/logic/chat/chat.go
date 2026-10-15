package chat

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gctx"
	"github.com/wangle201210/go-rag/server/internal/dao"
)

var (
	chat     *Chat
	chatMu   sync.RWMutex
	chatInit bool
)

type Chat struct {
	cm model.BaseChatModel
	eh messageSaver // messageSaver 接口支持测试注入；生产使用 *historyWrapper
}

// messageSaver 定义消息存储接口，生产实现为 *historyWrapper。
// 接口化允许测试注入 fake 实现，不依赖真实数据库。
type messageSaver interface {
	SaveMessage(mess *schema.Message, convID string) error
	GetHistory(convID string, limit int) ([]*schema.Message, error)
}

// ChatModelFactory 创建 BaseChatModel 的工厂接口
// 生产环境使用 defaultChatModelFactory，测试使用 fake
type ChatModelFactory interface {
	Create(apiKey, baseURL, modelName string) (model.BaseChatModel, error)
}

// defaultChatModelFactory 生产环境的 ChatModelFactory 实现
type defaultChatModelFactory struct{}

func (f *defaultChatModelFactory) Create(apiKey, baseURL, modelName string) (model.BaseChatModel, error) {
	return openai.NewChatModel(context.Background(), &openai.ChatModelConfig{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   modelName,
	})
}

// GetChat 返回 Chat 服务实例。未初始化时返回 nil。
func GetChat() *Chat {
	chatMu.RLock()
	defer chatMu.RUnlock()
	if !chatInit {
		return nil
	}
	return chat
}

// cfgGet 安全读取配置，传播错误而非忽略。
func cfgGet(ctx context.Context, key string) (string, error) {
	v, err := g.Cfg().Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("failed to read config %q: %w", key, err)
	}
	return v.String(), nil
}

// Init 初始化 Chat 服务（生产入口，等价于 InitWith(nil, nil)）。
// 必须先初始化 dao，因为 chat 依赖 dao.GetGormDB()。
// 重复调用返回 error（幂等拒绝）。
//
// History 适配器复用 dao 的 *gorm.DB 连接，不创建独立连接。
// chat-history 表的迁移通过 MigrateChatHistoryTables() 显式控制。
func Init() error {
	return InitWith(nil, nil)
}

// InitWith 初始化 Chat 服务：使用注入的配置和工厂。
// chatCfg: 注入的配置（nil 时从 g.Cfg() 读取）；
// cmFactory: 注入的 ChatModel 工厂（nil 时使用默认工厂）。
// 重复调用返回 error（幂等拒绝）。
func InitWith(chatCfg *ChatConfig, cmFactory ChatModelFactory) error {
	chatMu.Lock()
	defer chatMu.Unlock()

	if chatInit {
		return fmt.Errorf("chat: Init already called")
	}

	ctx := gctx.New()

	var apiKey, baseURL, chatModel string

	if chatCfg != nil {
		apiKey = chatCfg.APIKey
		baseURL = chatCfg.BaseURL
		chatModel = chatCfg.Model
	} else {
		var err error
		apiKey, err = cfgGet(ctx, "chat.apiKey")
		if err != nil {
			return fmt.Errorf("chat: %w", err)
		}
		baseURL, err = cfgGet(ctx, "chat.baseURL")
		if err != nil {
			return fmt.Errorf("chat: %w", err)
		}
		chatModel, err = cfgGet(ctx, "chat.model")
		if err != nil {
			return fmt.Errorf("chat: %w", err)
		}
	}

	if apiKey == "" {
		return fmt.Errorf("chat: chat.apiKey not configured")
	}
	if baseURL == "" {
		return fmt.Errorf("chat: chat.baseURL not configured")
	}
	if chatModel == "" {
		return fmt.Errorf("chat: chat.model not configured")
	}

	// 验证 dao 已初始化（chat 依赖 dao），在工厂调用之前检查
	_, err := dao.GetDsn()
	if err != nil {
		return fmt.Errorf("chat: dao.GetDsn() failed (is dao initialized?): %w", err)
	}

	// 如果未注入工厂，使用默认工厂
	if cmFactory == nil {
		cmFactory = &defaultChatModelFactory{}
	}

	// 使用注入的工厂创建 chat model
	cm, err := cmFactory.Create(apiKey, baseURL, chatModel)
	if err != nil {
		return fmt.Errorf("chat: create chat model failed: %w", err)
	}

	// 获取 dao 的 GORM DB 实例，创建 History 包装器
	// 复用 dao 的连接，不创建独立连接，不执行 AutoMigrate
	gormDB, err := dao.GetGormDB()
	if err != nil {
		return fmt.Errorf("chat: dao.GetGormDB() failed: %w", err)
	}
	if gormDB == nil {
		return fmt.Errorf("chat: DAO initialized but GetGormDB returned nil")
	}

	c := &Chat{cm: cm}
	c.eh = newHistoryWrapper(gormDB)

	// 成功后才赋值全局变量（原子发布）
	chat = c
	chatInit = true
	return nil
}

// ChatConfig 注入的 Chat 配置
type ChatConfig struct {
	APIKey  string
	BaseURL string
	Model   string
}

// IsInitialized 返回 Chat 服务是否已初始化。
func IsInitialized() bool {
	chatMu.RLock()
	defer chatMu.RUnlock()
	return chatInit
}

// Reset 重置 Chat 服务状态，清理全局变量。用于初始化失败时的级联清理。
func Reset() {
	chatMu.Lock()
	defer chatMu.Unlock()
	chat = nil
	chatInit = false
}

func (x *Chat) GetAnswer(ctx context.Context, convID string, docs []*schema.Document, question string) (answer string, err error) {
	messages, err := x.docsMessages(ctx, convID, docs, question)
	if err != nil {
		return "", err
	}
	result, err := generate(ctx, x.cm, messages)
	if err != nil {
		return "", fmt.Errorf("生成答案失败: %w", err)
	}
	err = x.eh.SaveMessage(result, convID)
	if err != nil {
		g.Log().Error(ctx, "save assistant message err: %v", err)
		return
	}
	return result.Content, nil
}

// StreamResult carries the foreground SSE completion outcome to the save goroutine.
type StreamResult struct {
	Success bool // true only when SSE completed normally (EOF, no write/encode error)
}

// streamCoordinator manages the lifecycle of a streaming answer session.
// It coordinates the foreground SSE consumer and the background save goroutine.
type streamCoordinator struct {
	resultCh  chan StreamResult
	coordDone chan struct{}
	srs       [2]*schema.StreamReader[*schema.Message]
	cancel    context.CancelFunc
	savedOnce sync.Once // first cleanup call decides; repeated calls are no-ops
}

// cleanup cancels the derived context (to unblock the model), sends the foreground
// result, closes srs[0], and waits for the background goroutine to finish.
// It is safe to call repeatedly; only the first call decides the result (sync.Once protection).
func (c *streamCoordinator) cleanup(result StreamResult) {
	c.savedOnce.Do(func() {
		// Cancel derived context first — unblocks model if still running.
		// Background save condition uses original ctx (not derivedCtx),
		// so this does not affect the save decision.
		c.cancel()
		// Send foreground result to background goroutine
		c.resultCh <- result
		// Close foreground branch
		c.srs[0].Close()
		// Wait for background goroutine to finish
		<-c.coordDone
	})
}

// GetAnswerStream 流式生成答案。
// 创建派生 context（WithCancel），传递给模型，确保客户端断开/取消时模型调用能退出。
// 返回：
//   - *schema.StreamReader[*schema.Message]: 前台消费者使用的流分支
//   - func(StreamResult): 清理函数，必须由调用方在 SSE 完成后调用。
//     传入前台结果以决定是否保存 assistant 回答；同时关闭后台分支以释放上游资源。
//     cleanup 内部等待后台协调任务结束，然后释放派生 context。
//     可安全重复调用（第一次终态决定结果）。
//
// 保存契约：只有同时满足以下条件才保存一次完整 assistant：
//   - 后台正常 EOF（cleanEOF=true）
//   - 前台 SSE 正常完成（result.Success=true）
//   - 请求未取消（ctx.Err()==nil）
//   - 没有读取、编码、写入或提前关闭错误
//
// History 保存不接受 context 的边界如实保留：等待保存可能受数据库调用阻塞，
// 不宣称有界。
func (x *Chat) GetAnswerStream(ctx context.Context, convID string, docs []*schema.Document, question string) (answer *schema.StreamReader[*schema.Message], cleanup func(StreamResult), err error) {
	messages, err := x.docsMessages(ctx, convID, docs, question)
	if err != nil {
		return
	}
	// 创建派生 context，传递给模型；cleanup 内部释放
	derivedCtx, cancel := context.WithCancel(ctx)
	streamData, streamErr := stream(derivedCtx, x.cm, messages)
	if streamErr != nil {
		cancel() // 建流失败也释放派生 context
		return nil, nil, fmt.Errorf("生成答案失败: %w", streamErr)
	}
	srs := streamData.Copy(2)

	coord := &streamCoordinator{
		resultCh:  make(chan StreamResult, 1),
		coordDone: make(chan struct{}),
		srs:       [2]*schema.StreamReader[*schema.Message]{srs[0], srs[1]},
		cancel:    cancel,
	}

	// 后台保存 goroutine：消费 srs[1]，记录 cleanEOF，等待前台结果后决定是否保存。
	// 取消/超时/模型失败/提前关闭 → 不保存部分回答。
	go func() {
		defer close(coord.coordDone)
		defer srs[1].Close()

		var cleanEOF bool
		var fullMsgs []*schema.Message

		// 持续从后台分支读取，直到 EOF 或上游关闭。
		for {
			chunk, recvErr := srs[1].Recv()
			if recvErr != nil {
				if recvErr == io.EOF {
					cleanEOF = true
				}
				break // EOF 或错误；退出循环等待前台结果
			}
			if chunk == nil {
				break // nil chunk: treat as abnormal, cleanEOF stays false
			}
			fullMsgs = append(fullMsgs, chunk)
		}

		// 等待前台结果
		result := <-coord.resultCh

		// 严格保存条件：cleanEOF + 前台成功 + 未取消
		if cleanEOF && result.Success && ctx.Err() == nil {
			fullMsg, concatErr := schema.ConcatMessages(fullMsgs)
			if concatErr != nil {
				g.Log().Error(ctx, "error concatenating messages: %v", concatErr)
				return
			}
			if saveErr := x.eh.SaveMessage(fullMsg, convID); saveErr != nil {
				g.Log().Error(ctx, "save assistant message err: %v", saveErr)
			}
		}
		// 非严格条件满足：不保存
	}()

	return srs[0], coord.cleanup, nil
}

func generate(ctx context.Context, llm model.BaseChatModel, in []*schema.Message) (message *schema.Message, err error) {
	message, err = llm.Generate(ctx, in)
	if err != nil {
		err = fmt.Errorf("llm generate failed: %v", err)
		return
	}
	return
}

func stream(ctx context.Context, llm model.BaseChatModel, in []*schema.Message) (res *schema.StreamReader[*schema.Message], err error) {
	res, err = llm.Stream(ctx, in)
	if err != nil {
		err = fmt.Errorf("llm generate failed: %w", err)
		return
	}
	return
}
