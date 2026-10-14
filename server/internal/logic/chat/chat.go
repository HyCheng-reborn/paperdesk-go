package chat

import (
	"context"
	"fmt"
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
	Success bool   // true only when SSE completed normally (EOF, no write/encode error)
	Answer  string // concatenated assistant content (empty on failure)
}

// GetAnswerStream 流式生成答案。
// 使用调用方 ctx 传递给模型，确保客户端断开/取消时模型调用能退出。
// 返回：
//   - *schema.StreamReader[*schema.Message]: 前台消费者使用的流分支
//   - func(StreamResult): 清理函数，必须由调用方在 SSE 完成后调用（defer）。
//     传入前台结果以决定是否保存 assistant 回答；同时关闭后台分支以释放上游资源。
//
// 保存契约：只有前台成功 (Success=true) 且 ctx 未取消时才保存完整回答。
// 后台 EOF 不足以触发保存；必须等待前台结果通知。
func (x *Chat) GetAnswerStream(ctx context.Context, convID string, docs []*schema.Document, question string) (answer *schema.StreamReader[*schema.Message], cleanup func(StreamResult), err error) {
	messages, err := x.docsMessages(ctx, convID, docs, question)
	if err != nil {
		return
	}
	// 使用调用方 ctx（不再替换为 context.Background()）
	streamData, err := stream(ctx, x.cm, messages)
	if err != nil {
		return nil, nil, fmt.Errorf("生成答案失败: %w", err)
	}
	srs := streamData.Copy(2)

	// resultCh 携带前台 SSE 完成结果，通知后台 goroutine 是否保存。
	resultCh := make(chan StreamResult, 1)

	// 后台保存 goroutine：消费 srs[1]，等待前台结果后决定是否保存。
	// 取消/超时/模型失败/提前关闭 → 不保存部分回答。
	go func() {
		fullMsgs := make([]*schema.Message, 0)

		// 持续从后台分支读取，直到 EOF 或上游关闭。
		// 这确保即使前台已完成，后台仍消费完所有模型输出，避免管道阻塞。
		for {
			chunk, recvErr := srs[1].Recv()
			if recvErr != nil {
				break // EOF 或错误；退出循环等待前台结果
			}
			fullMsgs = append(fullMsgs, chunk)
		}

		// 等待前台结果或 context 取消
		select {
		case result := <-resultCh:
			if result.Success && ctx.Err() == nil {
				fullMsg, concatErr := schema.ConcatMessages(fullMsgs)
				if concatErr != nil {
					g.Log().Error(ctx, "error concatenating messages: %v", concatErr)
					return
				}
				if saveErr := x.eh.SaveMessage(fullMsg, convID); saveErr != nil {
					g.Log().Error(ctx, "save assistant message err: %v", saveErr)
				}
			}
			// 非 Success 或 ctx 已取消：不保存
		case <-ctx.Done():
			// 取消，不保存
		}

		// 清理：关闭后台分支。若前台也已关闭，parent 看到所有 child 关闭，
		// 关闭上游，模型 Send 观察到流关闭并退出。
		srs[1].Close()
	}()

	cleanupFn := func(result StreamResult) {
		// 通知后台 goroutine 前台结果
		select {
		case resultCh <- result:
		default:
		}
		// 关闭前台分支。与后台分支的 Close 一起，parent 关闭上游。
		srs[0].Close()
	}

	return srs[0], cleanupFn, nil
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
