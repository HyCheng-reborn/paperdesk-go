package common

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/google/uuid"
)

// errWriteFailed is returned when a previous SSE write has failed.
var errWriteFailed = errors.New("sse: previous write failed")

// StreamData represents the SSE event payload.
type StreamData struct {
	Id       string             `json:"id"`
	Created  int64              `json:"created"`
	Content  string             `json:"content"`
	Document []*schema.Document `json:"document"`
}

// sseWriter is the interface for SSE writing (allows test injection).
type sseWriter interface {
	writeDocuments(data string) error
	writeData(jsonData string) error
	writeDone() error
	writeError(err error) error
}

// jsonEncoder is the interface for JSON encoding (allows test injection).
type jsonEncoder interface {
	Marshal(v any) ([]byte, error)
}

// realSSEWriter wraps http.ResponseWriter for production SSE writing.
type realSSEWriter struct {
	w      http.ResponseWriter
	failed bool
}

// writeFrame writes one raw SSE frame and flushes. Frame layout is chosen by
// each caller to stay compatible with the existing frontend parser.
func (s *realSSEWriter) writeFrame(frame string) error {
	if s.failed {
		return errWriteFailed
	}
	n, err := s.w.Write([]byte(frame))
	if err != nil {
		s.failed = true
		return fmt.Errorf("sse write failed: %w", err)
	}
	// Check for short write
	if n != len(frame) {
		s.failed = true
		return io.ErrShortWrite
	}
	// NOTE: 刷新错误检测边界——标准 http.Flusher.Flush() 无返回值，
	// 部分刷新（flush）错误无法在此检测。GoFrame RawWriter / ResponseController
	// 的刷新错误支持取决于底层实现，当前仅记录此已知边界，不宣称已全面验证。
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// writeDocuments emits the frontend-recognized documents frame:
// "documents:<JSON>\n\n". The Vue chat page extracts references from a line
// beginning with the literal "documents:" prefix, so this prefix must be kept.
func (s *realSSEWriter) writeDocuments(data string) error {
	return s.writeFrame("documents:" + data + "\n\n")
}

// writeData emits a content frame: "data:<JSON>\n\n".
func (s *realSSEWriter) writeData(jsonData string) error {
	return s.writeFrame("data:" + jsonData + "\n\n")
}

// writeDone emits the stream terminator: "data:[DONE]\n\n".
func (s *realSSEWriter) writeDone() error {
	return s.writeFrame("data:[DONE]\n\n")
}

// writeError keeps the original error frame format: "event: error\ndata: ...".
func (s *realSSEWriter) writeError(err error) error {
	// Safely encode error text (no line breaks)
	safeErr := strings.ReplaceAll(err.Error(), "\n", " ")
	safeErr = strings.ReplaceAll(safeErr, "\r", " ")
	return s.writeFrame("event: error\ndata: " + safeErr + "\n\n")
}

// streamResponse is the shared orchestration function called by both production and tests.
// It handles the complete SSE streaming flow: documents → data chunks → DONE/error.
// ctx is used to detect cancellation: if the request context is cancelled when the
// reader returns EOF, no [DONE] is sent and ctx.Err() is returned instead.
// A nil ctx is treated as non-cancellable (for backward-compatible tests).
func streamResponse(ctx context.Context, w sseWriter, reader *schema.StreamReader[*schema.Message], docs []*schema.Document, enc jsonEncoder) error {
	sd := &StreamData{
		Id:      uuid.NewString(),
		Created: time.Now().Unix(),
	}

	// Send documents event if present
	if len(docs) > 0 {
		sd.Document = docs
		marshal, err := enc.Marshal(sd)
		if err != nil {
			return fmt.Errorf("encode documents failed: %w", err)
		}
		if writeErr := w.writeDocuments(string(marshal)); writeErr != nil {
			return writeErr
		}
	}
	sd.Document = nil // Clear after sending once

	// Stream chunks
	for {
		// 读取前检查取消
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}

		chunk, recvErr := reader.Recv()

		// 读取后检查取消
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}

		if recvErr == io.EOF {
			// Normal EOF: only send DONE if the request context is not cancelled.
			if ctx != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			if writeErr := w.writeDone(); writeErr != nil {
				return writeErr
			}
			return nil
		}
		if recvErr != nil {
			// Non-EOF error: send error event, preserve both error chains
			if writeErr := w.writeError(recvErr); writeErr != nil {
				return errors.Join(recvErr, writeErr)
			}
			return recvErr
		}
		if chunk == nil {
			return fmt.Errorf("nil chunk received")
		}
		if len(chunk.Content) == 0 {
			continue // Skip empty content
		}

		// 写入前检查取消
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}

		sd.Content = chunk.Content
		marshal, err := enc.Marshal(sd)
		if err != nil {
			return fmt.Errorf("encode content failed: %w", err)
		}
		if writeErr := w.writeData(string(marshal)); writeErr != nil {
			return writeErr
		}
	}
}

// SteamResponse streams SSE responses from a StreamReader (production entry point).
// Only normal EOF sends [DONE]; non-EOF errors send an error event without DONE.
func SteamResponse(ctx context.Context, streamReader *schema.StreamReader[*schema.Message], docs []*schema.Document) error {
	httpReq := ghttp.RequestFromCtx(ctx)
	httpResp := httpReq.Response
	// Set SSE headers
	httpResp.Header().Set("Content-Type", "text/event-stream")
	httpResp.Header().Set("Cache-Control", "no-cache")
	httpResp.Header().Set("Connection", "keep-alive")
	httpResp.Header().Set("X-Accel-Buffering", "no") // Disable Nginx buffering
	httpResp.Header().Set("Access-Control-Allow-Origin", "*")

	sw := &realSSEWriter{w: httpResp.RawWriter()}
	// Use sonic encoder (same as tests)
	return streamResponse(ctx, sw, streamReader, docs, sonic.ConfigDefault)
}
