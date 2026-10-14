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

// writeEvent writes a standard SSE event frame and flushes.
func (s *realSSEWriter) writeEvent(event, data string) error {
	if s.failed {
		return errWriteFailed
	}
	// Standard SSE frame format with double newline boundary
	frame := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
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
	// NOTE: 标准 http.Flusher 不返回刷新错误。
	// GoFrame RawWriter 和 ResponseController 的刷新错误支持取决于底层实现。
	// 当前无法检测所有刷新错误，仅记录此边界。
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

func (s *realSSEWriter) writeDocuments(data string) error {
	return s.writeEvent("documents", data)
}

func (s *realSSEWriter) writeData(jsonData string) error {
	return s.writeEvent("data", jsonData)
}

func (s *realSSEWriter) writeDone() error {
	return s.writeEvent("data", "[DONE]")
}

func (s *realSSEWriter) writeError(err error) error {
	// Safely encode error text (no line breaks)
	safeErr := strings.ReplaceAll(err.Error(), "\n", " ")
	safeErr = strings.ReplaceAll(safeErr, "\r", " ")
	return s.writeEvent("error", safeErr)
}

// streamResponse is the shared orchestration function called by both production and tests.
// It handles the complete SSE streaming flow: documents → data chunks → DONE/error.
func streamResponse(w sseWriter, reader *schema.StreamReader[*schema.Message], docs []*schema.Document, enc jsonEncoder) error {
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
		chunk, recvErr := reader.Recv()
		if recvErr == io.EOF {
			// Normal completion: send DONE
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
	return streamResponse(sw, streamReader, docs, sonic.ConfigDefault)
}
