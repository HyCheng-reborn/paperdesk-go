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
	"github.com/wangle201210/chat-history/eino"
	"github.com/wangle201210/go-rag/server/internal/dao"
)

var (
	chat    *Chat
	chatMu  sync.Mutex
	chatInit bool
)

type Chat struct {
	cm model.BaseChatModel
	eh *eino.History
}

// GetChat 返回 Chat 服务实例。未初始化时返回 nil。
func GetChat() *Chat {
	chatMu.Lock()
	defer chatMu.Unlock()
	if !chatInit {
		return nil
	}
	return chat
}

// Init 初始化 Chat 服务：读取配置、创建 chat model、初始化 chat-history。
// 必须在程序启动时显式调用，不在 init() 中自动执行，以支持离线测试隔离。
// 注意：dao 必须先初始化，因为 chat 依赖 dao.GetDsn()。
// 重复调用返回 error（幂等拒绝）。
//
// chat-history 的 NewEinoHistory(dsn) 内部会对 chat-history 自己的 4 张表执行
// AutoMigrate（conversations, messages, attachments, message_attachments）。
// 这是第三方包行为，当前无法跳过；后续可在 Stage 5 通过适配器替换。
func Init() error {
	chatMu.Lock()
	defer chatMu.Unlock()

	if chatInit {
		return fmt.Errorf("chat: Init already called")
	}

	ctx := gctx.New()

	// 使用局部变量构建完整实例
	c, err := newChat(&openai.ChatModelConfig{
		APIKey:  g.Cfg().MustGet(ctx, "chat.apiKey").String(),
		BaseURL: g.Cfg().MustGet(ctx, "chat.baseURL").String(),
		Model:   g.Cfg().MustGet(ctx, "chat.model").String(),
	})
	if err != nil {
		return fmt.Errorf("newChat failed: %w", err)
	}
	// 使用 DSN 初始化，chat-history 包会根据 DSN 判断数据库类型
	// 对于 SQLite: file.db?_journal_mode=WAL
	// 对于 MySQL: user:pass@tcp(host:port)/dbname?charset=utf8mb4
	c.eh = eino.NewEinoHistory(dao.GetDsn())

	// 成功后才赋值全局变量（原子发布）
	chat = c
	chatInit = true
	return nil
}

func newChat(cfg *openai.ChatModelConfig) (res *Chat, err error) {
	chatModel, err := openai.NewChatModel(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	return &Chat{cm: chatModel}, nil
}

// IsInitialized 返回 Chat 服务是否已初始化。
func IsInitialized() bool {
	chatMu.Lock()
	defer chatMu.Unlock()
	return chatInit
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

// GetAnswerStream 流式生成答案
func (x *Chat) GetAnswerStream(ctx context.Context, convID string, docs []*schema.Document, question string) (answer *schema.StreamReader[*schema.Message], err error) {
	messages, err := x.docsMessages(ctx, convID, docs, question)
	if err != nil {
		return
	}
	ctx = context.Background()
	streamData, err := stream(ctx, x.cm, messages)
	if err != nil {
		return nil, fmt.Errorf("生成答案失败: %w", err)
	}
	srs := streamData.Copy(2)
	go func() {
		// for save
		fullMsgs := make([]*schema.Message, 0)
		defer func() {
			srs[1].Close()
			fullMsg, err := schema.ConcatMessages(fullMsgs)
			if err != nil {
				g.Log().Error(ctx, "error concatenating messages: %v", err)
				return
			}
			err = x.eh.SaveMessage(fullMsg, convID)
			if err != nil {
				g.Log().Error(ctx, "save assistant message err: %v", err)
				return
			}
		}()
	outer:
		for {
			select {
			case <-ctx.Done():
				fmt.Println("context done", ctx.Err())
				return
			default:
				chunk, err := srs[1].Recv()
				if err != nil {
					if errors.Is(err, io.EOF) {
						break outer
					}
				}
				fullMsgs = append(fullMsgs, chunk)
			}
		}
	}()

	return srs[0], nil

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
		err = fmt.Errorf("llm generate failed: %v", err)
		return
	}
	return
}
