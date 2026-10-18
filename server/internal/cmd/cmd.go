package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/ThinkInAIXYZ/go-mcp/server"
	"github.com/ThinkInAIXYZ/go-mcp/transport"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/os/gcmd"
	"github.com/wangle201210/go-rag/server/core"
	"github.com/wangle201210/go-rag/server/internal/controller/rag"
	"github.com/wangle201210/go-rag/server/internal/dao"
	"github.com/wangle201210/go-rag/server/internal/logic/chat"
	ragicit "github.com/wangle201210/go-rag/server/internal/logic/rag"
	"github.com/wangle201210/go-rag/server/internal/mcp"
)

// funcCloser 将 func() error 包装为 io.Closer，用于关闭协调器
type funcCloser struct {
	fn func() error
}

func (fc *funcCloser) Close() error {
	if fc.fn != nil {
		return fc.fn()
	}
	return nil
}

var (
	Main = gcmd.Command{
		Name:  "main",
		Usage: "main",
		Brief: "start http server",
		Func: func(ctx context.Context, parser *gcmd.Parser) (err error) {
			// 显式初始化（顺序重要：dao → rag → chat）
			// 注意：普通启动不执行 Migrate，避免意外改表。
			// 需要迁移时使用 dao.InitWithMigrate() 或 dao.Migrate()。
			// 失败时 RunInitSteps 级联清理已建立资源，返回带阶段信息的错误。
			steps := []InitStep{
				{Name: "dao", Fn: dao.InitDBWithConfig, Cleanup: dao.Reset},
				{Name: "rag", Fn: ragicit.InitWithRealDeps, Cleanup: ragicit.Reset},
				{Name: "chat", Fn: chat.Init, Cleanup: func() error { chat.Reset(); return nil }},
			}
			if err := RunInitSteps(steps); err != nil {
				return fmt.Errorf("initialization failed: %w", err)
			}

			s := g.Server()
			Mcp(ctx, s)
			s.Group("/", func(group *ghttp.RouterGroup) {
				s.AddStaticPath("", "./static/fe/")
				s.SetIndexFiles([]string{"index.html"})
			})
			s.Group("/api", func(group *ghttp.RouterGroup) {
				group.Middleware(MiddlewareHandlerResponse, ghttp.MiddlewareCORS)
				group.Bind(
					rag.NewV1(),
				)
			})
			s.Run()

			// s.Run() 返回后执行关闭协调
			// 使用 30s 超时等待后台任务收敛
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer shutdownCancel()

			// 获取 Rag 实例
			ragInstance := ragicit.GetRagSvr()
			if ragInstance == nil {
				g.Log().Warning(ctx, "Rag instance is nil, skipping shutdown coordination")
				return nil
			}

			// 创建关闭协调器：dao 先初始化，最后关闭
			// ragicit.Reset 和 dao.Close 包装为 io.Closer
			raoResetCloser := &funcCloser{fn: ragicit.Reset}
			daoCloser := &funcCloser{fn: dao.Close}
			coordinator := core.NewShutdownCoordinator(ragInstance, daoCloser, raoResetCloser)

			if err := coordinator.Shutdown(shutdownCtx); err != nil {
				g.Log().Errorf(ctx, "shutdown coordination failed: %v", err)
				return err // 不报告优雅关闭完成
			}

			return nil
		},
	}
)

func Mcp(ctx context.Context, s *ghttp.Server) {
	trans, handler, err := transport.NewStreamableHTTPServerTransportAndHandler()
	if err != nil {
		g.Log().Panicf(ctx, "new sse transport and hander with error: %v", err)
	}
	// new mcp server
	mcpServer, _ := server.NewServer(trans)
	mcpServer.RegisterTool(mcp.GetRetrieverTool(), mcp.HandleRetriever)
	mcpServer.RegisterTool(mcp.GetKnowledgeBaseTool(), mcp.HandleKnowledgeBase)
	mcpServer.RegisterTool(mcp.GetWebSearchTool(), mcp.HandleWebSearch)
	// start mcp Server
	go func() {
		mcpServer.Run()
	}()
	// mcpServer.Shutdown(context.Background())
	s.Group("/", func(r *ghttp.RouterGroup) {
		r.ALL("/mcp", func(r *ghttp.Request) {
			handler.HandleMCP().ServeHTTP(r.Response.Writer, r.Request)
		})
	})
}
