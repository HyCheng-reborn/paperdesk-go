package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gctx"
	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/providers"
	"github.com/wangle201210/go-rag/server/internal/dao"
)

var (
	chat     *Chat
	chatMu   sync.RWMutex
	chatInit bool
)

type Chat struct {
	cm        model.BaseChatModel
	eh        messageSaver // messageSaver 接口支持测试注入；生产使用 *historyWrapper
	OnBGReady func()       // test hook: called when background goroutine is waiting for result (nil in production)
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
	// B4: Registry 优先；失败时传播错误，不回退到旧路径
	if r := common.GetRegistry(); r != nil {
		chatRole, err := r.GetChat(context.Background())
		if err != nil {
			return nil, fmt.Errorf("registry GetChat: %w", err)
		}
		return chatRole, nil
	}
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
	// B5: 请求入口注入 RequestBudget，使下游 Chat 适配器共享同一累计 owner
	ctx = common.WithRequestBudget(ctx)
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
	coordDone chan struct{} // closed when background goroutine exits
	bgReady   chan struct{} // closed when background has consumed EOF and is waiting for result

	// Session path: independent Pipe fork, avoids Copy(2) double-close
	session     *providers.StreamSession              // nil for fallback path
	fgCleanupCh *schema.StreamReader[*schema.Message] // cleanup's Copy(2) child for closing fgPipe

	// Fallback path: Copy(2) branches
	srs    [2]*schema.StreamReader[*schema.Message]
	cancel context.CancelFunc // fallback path: derivedCtx cancel

	savedOnce sync.Once // first cleanup call decides; repeated calls are no-ops
}

// cleanupFail: cancel model first (unblock if still running), then notify
// failure, close foreground branch, and wait for background goroutine.
// Session 路径：关闭 fgPipe → Drain session → 通知失败 → 等待后台。
// 回退路径：cancel → 通知失败 → 关闭前台分支 → 等待后台。
func (c *streamCoordinator) cleanupFail() {
	c.savedOnce.Do(func() {
		if c.session != nil {
			c.session.Drain() // 1. cancel + close session → forwarding goroutine exits, closes fgWriter
			if c.fgCleanupCh != nil {
				c.fgCleanupCh.Close() // 2. close cleanup's Copy(2) child → propagates to parent when all children closed
			}
			c.resultCh <- StreamResult{} // 3. notify failure
			<-c.coordDone                // 4. wait background
		} else {
			c.cancel()                   // 1. unblock model
			c.resultCh <- StreamResult{} // 2. notify failure
			c.srs[0].Close()             // 3. close foreground branch
			<-c.coordDone                // 4. wait background
		}
	})
}

// cleanupOK: notify success, let background complete EOF consumption and
// save decision, wait for completion, close the foreground branch, then
// cancel the derived context.
// Session 路径：通知成功 → 等待后台保存 → 关闭 fgPipe → Close session。
// 回退路径：通知成功 → 等待后台 → 关闭前台分支 → cancel。
func (c *streamCoordinator) cleanupOK() {
	c.savedOnce.Do(func() {
		if c.session != nil {
			c.resultCh <- StreamResult{Success: true} // 1. notify success
			<-c.coordDone                             // 2. wait background: consume eof, save, exit
			if c.fgCleanupCh != nil {
				c.fgCleanupCh.Close() // 3. close cleanup's Copy(2) child
			}
			c.session.Close() // 4. cancel + close session reader
		} else {
			c.resultCh <- StreamResult{Success: true} // 1. notify success
			<-c.coordDone                             // 2. wait background
			c.srs[0].Close()                          // 3. close foreground branch
			c.cancel()                                // 4. release derived context
		}
	})
}

// GetAnswerStream 流式生成答案。
// 创建派生 context（WithCancel），传递给模型，确保客户端断开/取消时模型调用能退出。
// 返回：
//   - *schema.StreamReader[*schema.Message]: 前台消费者使用的流分支
//   - func(StreamResult): 清理函数，必须由调用方在 SSE 完成后调用。
//     Success=true 时走 cleanupOK：通知成功→等待后台完成 EOF 消费与保存→再 cancel。
//     Success=false 时走 cleanupFail：先 cancel 取消模型→通知失败→关闭前台→等待后台。
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
	// B5: 请求入口注入 RequestBudget，子步骤（模型调用/流式消费）共享同一累计 owner
	ctx = common.WithRequestBudget(ctx)
	messages, err := x.docsMessages(ctx, convID, docs, question)
	if err != nil {
		return
	}

	// 尝试使用 StreamSessioner 接口（受控流会话路径）
	if ss, ok := x.cm.(providers.StreamSessioner); ok {
		session, sErr := ss.StreamSession(ctx, messages)
		if sErr != nil {
			return nil, nil, fmt.Errorf("生成答案失败: %w", sErr)
		}
		return x.setupSessionCoordinator(ctx, session, convID)
	}

	// 回退路径：Copy(2) + 旧协调器
	derivedCtx, cancel := context.WithCancel(ctx)
	streamData, streamErr := stream(derivedCtx, x.cm, messages)
	if streamErr != nil {
		cancel()
		return nil, nil, fmt.Errorf("生成答案失败: %w", streamErr)
	}
	srs := streamData.Copy(2)

	coord := &streamCoordinator{
		resultCh:  make(chan StreamResult, 1),
		coordDone: make(chan struct{}),
		bgReady:   make(chan struct{}),
		srs:       [2]*schema.StreamReader[*schema.Message]{srs[0], srs[1]},
		cancel:    cancel,
	}

	// 后台保存 goroutine：消费 srs[1]，记录 cleanEOF，等待前台结果后决定是否保存。
	go func() {
		defer close(coord.coordDone)
		defer srs[1].Close()

		var cleanEOF bool
		var fullMsgs []*schema.Message

		for {
			chunk, recvErr := srs[1].Recv()
			if recvErr != nil {
				if recvErr == io.EOF {
					cleanEOF = true
				}
				break
			}
			if chunk == nil {
				break
			}
			fullMsgs = append(fullMsgs, chunk)
		}

		close(coord.bgReady)
		if x.OnBGReady != nil {
			x.OnBGReady()
		}
		result := <-coord.resultCh

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
	}()

	return srs[0], func(r StreamResult) {
		if r.Success {
			coord.cleanupOK()
		} else {
			coord.cleanupFail()
		}
	}, nil
}

// setupSessionCoordinator 使用 StreamSession 设置协调器。
// 不使用 Copy(2)（避免 parent/child reader 的 double-close panic），
// 而是创建两个独立 Pipe：转发 goroutine 从 session.Reader() 读取并写入两个 Pipe writer，
// 前台和后台各使用独立的 Pipe reader。
//
// 关闭所有权：
//   - fgPipe（前台 Pipe reader）由 cleanup 关闭
//   - bgPipe（后台 Pipe reader）由后台 goroutine defer 关闭
//   - 转发 goroutine 的 Pipe writer 在其退出时自动关闭
//   - session 由 cleanup 在后台退出后关闭（cancel + close reader + wait forwarding）
func (x *Chat) setupSessionCoordinator(ctx context.Context, session *providers.StreamSession, convID string) (*schema.StreamReader[*schema.Message], func(StreamResult), error) {
	// Create two independent Pipes: one for foreground, one for background.
	fgPipe, fgWriter := schema.Pipe[*schema.Message](8)
	bgPipe, bgWriter := schema.Pipe[*schema.Message](8)

	// Copy(2) on fgPipe creates two child readers. Each child's Close is independent;
	// the parent only closes when ALL children are closed. This prevents double-close panic
	// when both consumer and cleanup close their respective children.
	fgChildren := fgPipe.Copy(2)

	coord := &streamCoordinator{
		resultCh:    make(chan StreamResult, 1),
		coordDone:   make(chan struct{}),
		bgReady:     make(chan struct{}),
		session:     session,
		fgCleanupCh: fgChildren[1], // cleanup closes this child
	}

	// Forwarding goroutine: read from session reader, write to two Pipes.
	// fgWriter is ALWAYS closed by defer when this goroutine exits (any path).
	// bgWriter is ALWAYS closed by defer when this goroutine exits.
	// This ensures both consumers get EOF signal without cleanup needing to close fgPipe.
	go func() {
		defer fgWriter.Close() // ensure foreground always gets EOF/close signal
		defer bgWriter.Close() // ensure background always gets EOF/close signal
		sr := session.Reader()
		for {
			chunk, err := sr.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					// Non-EOF error: propagate to both branches
					fgWriter.Send(nil, err)
					bgWriter.Send(nil, err)
				}
				return // defer closes both writers
			}
			fgClosed := fgWriter.Send(chunk, nil)
			bgWriter.Send(chunk, nil)
			if fgClosed {
				return // consumer closed fgPipe, defer closes bgWriter
			}
		}
	}()

	// 后台保存 goroutine：从 bgPipe 读取
	go func() {
		defer close(coord.coordDone)
		defer bgPipe.Close()

		var cleanEOF bool
		var fullMsgs []*schema.Message

		for {
			chunk, recvErr := bgPipe.Recv()
			if recvErr != nil {
				if recvErr == io.EOF {
					cleanEOF = true
				}
				break
			}
			if chunk == nil {
				break
			}
			fullMsgs = append(fullMsgs, chunk)
		}

		close(coord.bgReady)
		if x.OnBGReady != nil {
			x.OnBGReady()
		}
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
	}()

	return fgChildren[0], func(r StreamResult) {
		if r.Success {
			coord.cleanupOK()
		} else {
			coord.cleanupFail()
		}
	}, nil
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

// InjectForTest sets the Chat model and message saver directly.
// For testing only: allows controller tests to use real Chat.GetAnswerStream
// with fake dependencies without going through Init/DAO.
func InjectForTest(c *Chat, cm model.BaseChatModel, saver messageSaver) {
	c.cm = cm
	c.eh = saver
}
