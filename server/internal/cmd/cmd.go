package cmd

import (
	"context"
	"fmt"

	"github.com/ThinkInAIXYZ/go-mcp/server"
	"github.com/ThinkInAIXYZ/go-mcp/transport"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/os/gcmd"
	"github.com/wangle201210/go-rag/server/internal/controller/rag"
	"github.com/wangle201210/go-rag/server/internal/dao"
	"github.com/wangle201210/go-rag/server/internal/logic/chat"
	ragicit "github.com/wangle201210/go-rag/server/internal/logic/rag"
	"github.com/wangle201210/go-rag/server/internal/mcp"
)

var (
	Main = gcmd.Command{
		Name:  "main",
		Usage: "main",
		Brief: "start http server",
		Func: func(ctx context.Context, parser *gcmd.Parser) (err error) {
			// 显式初始化（顺序重要：dao → rag → chat）
			// 注意：普通启动不执行 Migrate，避免意外改表。
			// 需要迁移时使用 dao.InitWithMigrate() 或 dao.Migrate()。
			// 失败时级联清理已建立资源，返回带阶段信息的错误。
			if err := dao.InitDBWithConfig(); err != nil {
				return fmt.Errorf("stage[dao] init failed: %w", err)
			}
			if err := ragicit.Init(); err != nil {
				if resetErr := dao.Reset(); resetErr != nil {
					return fmt.Errorf("stage[rag] init failed: %w; dao reset also failed: %v", err, resetErr)
				}
				return fmt.Errorf("stage[rag] init failed: %w", err)
			}
			if err := chat.Init(); err != nil {
				var errs []error
				errs = append(errs, fmt.Errorf("stage[chat] init failed: %w", err))
				if resetErr := ragicit.Reset(); resetErr != nil {
					errs = append(errs, fmt.Errorf("rag reset failed: %w", resetErr))
				}
				if closeErr := dao.Close(); closeErr != nil {
					errs = append(errs, fmt.Errorf("dao close failed: %w", closeErr))
				}
				if len(errs) > 1 {
					return fmt.Errorf("%v", errs)
				}
				return errs[0]
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
