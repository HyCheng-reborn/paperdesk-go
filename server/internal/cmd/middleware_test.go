package cmd

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/util/guid"
	"github.com/wangle201210/go-rag/server/core/common"
)

// waitForServer waits for the GoFrame server to be ready.
func waitForServer(t *testing.T, port int) {
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 50; i++ {
		resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not start in time")
}

// TestMiddleware_SSEStarted_NoJSONAppend verifies through a real GoFrame HTTP server
// that when SSE has started, the middleware does NOT append a JSON error response.
func TestMiddleware_SSEStarted_NoJSONAppend(t *testing.T) {
	s := g.Server(guid.S())
	s.Group("/", func(group *ghttp.RouterGroup) {
		group.Middleware(MiddlewareHandlerResponse)
		group.GET("/test/stream", func(r *ghttp.Request) {
			// Create a stream that sends a chunk then errors
			sr, sw := schema.Pipe[*schema.Message](3)
			go func() {
				sw.Send(&schema.Message{Role: schema.Assistant, Content: "chunk1"}, nil)
				sw.Send(nil, errors.New("model_failure"))
				sw.Close()
			}()
			defer sr.Close()

			// Call actual SteamResponse
			err := common.SteamResponse(r.Context(), sr, nil)
			if err != nil {
				// Set error but don't write anything (SSE already started)
				r.SetError(err)
			}
		})
	})
	s.SetDumpRouterMap(false)
	s.SetPort(0) // random port
	if err := s.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer s.Shutdown()

	port := s.GetListenedPort()
	if port == 0 {
		t.Fatal("server not listening")
	}
	waitForServer(t, port)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/test/stream")
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	body := string(bodyBytes)

	// Content-Type should be text/event-stream, not application/json
	respCT := resp.Header.Get("Content-Type")
	if strings.Contains(respCT, "application/json") {
		t.Errorf("Content-Type should not be JSON after SSE started, got: %s", respCT)
	}
	if !strings.Contains(respCT, "text/event-stream") {
		t.Errorf("Content-Type should be text/event-stream, got: %s", respCT)
	}

	// Body should contain SSE data, not JSON error
	if strings.Contains(body, `"code"`) {
		t.Errorf("body should not contain JSON error code field, got: %s", body)
	}
	if strings.Contains(body, `"message"`) {
		t.Errorf("body should not contain JSON error message field, got: %s", body)
	}
	if !strings.Contains(body, "chunk1") {
		t.Errorf("body should contain SSE data 'chunk1', got: %s", body)
	}
	// Should contain error event
	if !strings.Contains(body, "event: error") {
		t.Errorf("body should contain error event, got: %s", body)
	}
	// Should NOT contain DONE after error
	if strings.Contains(body, "[DONE]") {
		t.Errorf("body should not contain [DONE] after error, got: %s", body)
	}
}

// TestMiddleware_NoSSE_WithError verifies that when SSE has NOT started
// and handler returns error, middleware DOES append JSON error.
func TestMiddleware_NoSSE_WithError(t *testing.T) {
	s := g.Server(guid.S())
	s.Group("/", func(group *ghttp.RouterGroup) {
		group.Middleware(MiddlewareHandlerResponse)
		group.GET("/test/error", func(r *ghttp.Request) {
			// Set error without starting SSE
			r.SetError(errors.New("handler error"))
		})
	})
	s.SetDumpRouterMap(false)
	s.SetPort(0)
	if err := s.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer s.Shutdown()

	port := s.GetListenedPort()
	waitForServer(t, port)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/test/error")
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	// Should be JSON response
	respCT := resp.Header.Get("Content-Type")
	if !strings.Contains(respCT, "application/json") {
		t.Errorf("Content-Type should be application/json, got: %s", respCT)
	}
	// Should contain error message
	if !strings.Contains(body, "handler error") {
		t.Errorf("body should contain error message, got: %s", body)
	}
	if !strings.Contains(body, `"code"`) {
		t.Errorf("body should contain JSON code field, got: %s", body)
	}
}
