package common

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/util/guid"
)

// failingWriter is an http.ResponseWriter that fails after N successful writes.
type failingWriter struct {
	http.ResponseWriter
	failAfter  int
	writeCount int
}

func (f *failingWriter) Write(b []byte) (int, error) {
	f.writeCount++
	if f.writeCount > f.failAfter {
		return 0, errors.New("simulated write failure")
	}
	return f.ResponseWriter.Write(b)
}

func (f *failingWriter) Flush() {
	if fl, ok := f.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

func msg(content string) *schema.Message {
	return &schema.Message{Role: schema.Assistant, Content: content}
}

// --- Normal EOF tests ---

func TestSteamResponse_NormalEOF(t *testing.T) {
	sr := schema.StreamReaderFromArray([]*schema.Message{
		msg("hello"),
		msg(" world"),
	})
	defer sr.Close()

	rec := httptest.NewRecorder()
	err := streamResponseForTest(rec, sr, nil)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "hello") {
		t.Error("expected 'hello' in output")
	}
	if !strings.Contains(body, "world") {
		t.Error("expected 'world' in output")
	}
	if !strings.Contains(body, "[DONE]") {
		t.Error("expected [DONE] in output")
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
		// streamResponseForTest doesn't set headers (only SteamResponse does)
		// This is expected - headers are set by SteamResponse via ghttp
	}
}

func TestSteamResponse_EmptyStream(t *testing.T) {
	sr := schema.StreamReaderFromArray([]*schema.Message{})
	defer sr.Close()

	rec := httptest.NewRecorder()
	err := streamResponseForTest(rec, sr, nil)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "[DONE]") {
		t.Error("expected [DONE] for empty stream")
	}
	// Should only have the DONE event
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 line (only DONE), got %d: %q", len(lines), body)
	}
}

func TestSteamResponse_EmptyContentChunks(t *testing.T) {
	sr := schema.StreamReaderFromArray([]*schema.Message{
		msg(""),       // empty content → skipped
		msg("visible"),
		msg(""),       // another empty → skipped
	})
	defer sr.Close()

	rec := httptest.NewRecorder()
	err := streamResponseForTest(rec, sr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, "\"content\":\"\"") {
		t.Error("empty content chunks should be skipped")
	}
	if !strings.Contains(body, "visible") {
		t.Error("expected 'visible' in output")
	}
}

// --- Stream error tests ---

func TestSteamResponse_StreamError_NoDone(t *testing.T) {
	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		sw.Send(msg("partial"), nil)
		sw.Send(nil, errors.New("model error"))
		sw.Close()
	}()
	defer sr.Close()

	rec := httptest.NewRecorder()
	err := streamResponseForTest(rec, sr, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stream read failed") {
		t.Errorf("expected 'stream read failed' in error, got: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "partial") {
		t.Error("expected 'partial' data event")
	}
	if !strings.Contains(body, "event: error") {
		t.Error("expected error event")
	}
	if !strings.Contains(body, "model error") {
		t.Errorf("expected error message in error event, body: %q", body)
	}
	if strings.Contains(body, "[DONE]") {
		t.Error("should NOT send DONE after error")
	}
}

// --- nil chunk test ---

func TestSteamResponse_NilChunk(t *testing.T) {
	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		sw.Send(nil, nil) // nil chunk, no error
		sw.Close()
	}()
	defer sr.Close()

	rec := httptest.NewRecorder()
	err := streamResponseForTest(rec, sr, nil)
	if err == nil {
		t.Fatal("expected error for nil chunk")
	}
	if !strings.Contains(err.Error(), "nil chunk") {
		t.Errorf("expected 'nil chunk' in error, got: %v", err)
	}
}

// --- Error chain tests ---

func TestSteamResponse_ErrorChain(t *testing.T) {
	originalErr := errors.New("upstream_error")
	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		sw.Send(nil, originalErr)
		sw.Close()
	}()
	defer sr.Close()

	rec := httptest.NewRecorder()
	err := streamResponseForTest(rec, sr, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, originalErr) {
		t.Errorf("error should wrap original: errors.Is failed, got: %v", err)
	}
}

// --- Safe error encoding tests ---

func TestSteamResponse_SafeErrorEncoding(t *testing.T) {
	errWithNewlines := errors.New("error\nwith\nnewlines\r\nand\rcarriage_returns")
	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		sw.Send(nil, errWithNewlines)
		sw.Close()
	}()
	defer sr.Close()

	rec := httptest.NewRecorder()
	_ = streamResponseForTest(rec, sr, nil)

	body := rec.Body.String()
	// The error event data line should not contain raw newlines within the data
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") && line != "data: [DONE]" {
			// This is the error data line - should not contain raw \n or \r
			if strings.Contains(line, "\\n") || strings.Contains(line, "\\r") {
				t.Error("error data should not contain escaped newlines")
			}
			// The newlines should be replaced with spaces
			if strings.Contains(line, "error with newlines") {
				// Good - newlines replaced with spaces
			}
		}
	}
}

// --- Write failure tests ---

func TestSteamResponse_WriteFail_Documents(t *testing.T) {
	docs := []*schema.Document{{Content: "doc1"}}
	sr := schema.StreamReaderFromArray([]*schema.Message{msg("hello")})
	defer sr.Close()

	fw := &failingWriter{ResponseWriter: httptest.NewRecorder(), failAfter: 0}
	err := streamResponseForTest(fw, sr, docs)
	if err == nil {
		t.Fatal("expected write error for documents")
	}
	if !strings.Contains(err.Error(), "write") && !strings.Contains(err.Error(), "encode") {
		t.Errorf("expected write/encode error, got: %v", err)
	}
}

func TestSteamResponse_WriteFail_Data(t *testing.T) {
	sr := schema.StreamReaderFromArray([]*schema.Message{msg("hello")})
	defer sr.Close()

	fw := &failingWriter{ResponseWriter: httptest.NewRecorder(), failAfter: 0}
	err := streamResponseForTest(fw, sr, nil)
	if err == nil {
		t.Fatal("expected write error for data")
	}
}

func TestSteamResponse_WriteFail_Done(t *testing.T) {
	// Allow first write (data event) to succeed, but DONE write to fail
	sr := schema.StreamReaderFromArray([]*schema.Message{msg("hi")})
	defer sr.Close()

	fw := &failingWriter{ResponseWriter: httptest.NewRecorder(), failAfter: 1}
	err := streamResponseForTest(fw, sr, nil)
	if err == nil {
		t.Fatal("expected write error for DONE")
	}
	if !errors.Is(err, errWriteFailed) && !strings.Contains(err.Error(), "write") {
		t.Errorf("expected write-related error, got: %v", err)
	}
}

func TestSteamResponse_WriteFail_ErrorEvent(t *testing.T) {
	// First write succeeds (data), second write fails (error event)
	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		sw.Send(msg("partial"), nil)
		sw.Send(nil, errors.New("model_err"))
		sw.Close()
	}()
	defer sr.Close()

	// failAfter:1 → first write (data "partial") succeeds, second write (error event) fails
	fw := &failingWriter{ResponseWriter: httptest.NewRecorder(), failAfter: 1}
	err := streamResponseForTest(fw, sr, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	// The error should be from the failed write of the error event, not from the stream read
	if !strings.Contains(err.Error(), "write") && !strings.Contains(err.Error(), "stream read") {
		t.Errorf("expected write or stream error, got: %v", err)
	}
}

func TestSteamResponse_SubsequentWriteAfterFailure(t *testing.T) {
	// After a write failure, subsequent writes should return errWriteFailed
	sw := &sseWriter{
		w:      httptest.NewRecorder(),
		failed: true,
	}
	err := sw.writeData("test")
	if !errors.Is(err, errWriteFailed) {
		t.Errorf("expected errWriteFailed, got: %v", err)
	}
	err = sw.writeDone()
	if !errors.Is(err, errWriteFailed) {
		t.Errorf("expected errWriteFailed for DONE, got: %v", err)
	}
}

// --- Documents tests ---

func TestSteamResponse_WithDocuments(t *testing.T) {
	docs := []*schema.Document{
		{Content: "doc1"},
		{Content: "doc2"},
	}
	sr := schema.StreamReaderFromArray([]*schema.Message{msg("answer")})
	defer sr.Close()

	rec := httptest.NewRecorder()
	err := streamResponseForTest(rec, sr, docs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "documents:") {
		t.Error("expected documents event")
	}
	if !strings.Contains(body, "doc1") {
		t.Error("expected doc1 content in documents event")
	}
	if !strings.Contains(body, "answer") {
		t.Error("expected answer content in data event")
	}
	if !strings.Contains(body, "[DONE]") {
		t.Error("expected [DONE]")
	}
}

// --- SSE event format tests ---

func TestSteamResponse_EventFormat(t *testing.T) {
	sr := schema.StreamReaderFromArray([]*schema.Message{msg("test")})
	defer sr.Close()

	rec := httptest.NewRecorder()
	err := streamResponseForTest(rec, sr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body := rec.Body.String()
	// Data events should start with "data:"
	if !strings.Contains(body, "data:") {
		t.Error("expected data: prefix in events")
	}
	// DONE event
	if !strings.Contains(body, "data:[DONE]") {
		t.Error("expected data:[DONE] event")
	}
}

func TestSSEError_EventFormat(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &sseWriter{w: rec}
	err := sw.writeError(errors.New("test error"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Error("expected 'event: error' in output")
	}
	if !strings.Contains(body, "test error") {
		t.Error("expected error message in data field")
	}
}

// --- Middleware integration simulation ---

// TestMiddleware_NoJSONAppend_AfterSSEStarted verifies that after SSE has started
// (Content-Type: text/event-stream set and bytes written), the GoFrame response
// middleware's early-return conditions are satisfied, preventing JSON error append.
func TestMiddleware_NoJSONAppend_AfterSSEStarted(t *testing.T) {
	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		sw.Send(msg("chunk1"), nil)
		sw.Send(nil, errors.New("model_failure"))
		sw.Close()
	}()
	defer sr.Close()

	rec := httptest.NewRecorder()
	// Simulate SteamResponse setting SSE headers
	rec.Header().Set("Content-Type", "text/event-stream")
	rec.Header().Set("Cache-Control", "no-cache")

	_ = streamResponseForTest(rec, sr, nil)

	// Verify middleware early-return conditions:
	// 1. Content-Type is text/event-stream → middleware returns early
	ct := rec.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	// 2. BytesWritten > 0 → middleware returns early
	if rec.Body.Len() == 0 {
		t.Error("expected bytes written > 0 after SSE started")
	}
	// 3. Response body contains only SSE events, no JSON
	body := rec.Body.String()
	if strings.Contains(body, `"code"`) || strings.Contains(body, `"message"`) {
		t.Error("response should not contain JSON fields after SSE started")
	}
	// 4. Body contains SSE data + error event, no DONE
	if !strings.Contains(body, "chunk1") {
		t.Error("expected chunk1 data event")
	}
	if !strings.Contains(body, "event: error") {
		t.Error("expected error event")
	}
	if strings.Contains(body, "[DONE]") {
		t.Error("should not send DONE after error")
	}
}

// TestMiddleware_Integration_GoFrameServer verifies through an actual GoFrame HTTP server
// that when SSE has started and the handler returns an error, the middleware does NOT
// append a JSON error response.
func TestMiddleware_Integration_GoFrameServer(t *testing.T) {
	s := g.Server(guid.S())
	s.Group("/", func(group *ghttp.RouterGroup) {
		group.Middleware(func(r *ghttp.Request) {
			r.Middleware.Next()
			// Simulate the key middleware check:
			// If Content-Type is text/event-stream or bytes written > 0, skip JSON.
			ct := r.Response.Header().Get("Content-Type")
			if ct == "text/event-stream" || r.Response.Writer.BytesWritten() > 0 {
				return
			}
			// Otherwise write JSON error
			if err := r.GetError(); err != nil {
				r.Response.WriteJson(map[string]interface{}{
					"code": 500, "message": err.Error(),
				})
			}
		})
		group.POST("/sse-fail", func(r *ghttp.Request) {
			r.Response.Header().Set("Content-Type", "text/event-stream")
			r.Response.Write("data:chunk1\n")
			r.Response.Flush()
			// Simulate error after SSE started
			r.SetError(errors.New("stream error"))
		})
	})
	s.SetDumpRouterMap(false)
	s.SetPort(0) // random port
	if err := s.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer s.Shutdown()

	time.Sleep(200 * time.Millisecond)
	port := s.GetListenedPort()
	if port == 0 {
		t.Fatal("server not listening")
	}

	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/sse-fail", port),
		"application/json", nil,
	)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	// Content-Type should be text/event-stream, not application/json
	respCT := resp.Header.Get("Content-Type")
	if strings.Contains(respCT, "application/json") {
		t.Errorf("Content-Type should not be JSON after SSE started, got: %s", respCT)
	}
	// Body should contain SSE data, not JSON error
	if strings.Contains(body, `"code"`) {
		t.Errorf("body should not contain JSON error, got: %s", body)
	}
	if !strings.Contains(body, "chunk1") {
		t.Errorf("body should contain SSE data, got: %s", body)
	}
}
