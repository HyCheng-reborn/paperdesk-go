package common

import (
	"context"
	"encoding/json"
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

type StreamData struct {
	Id       string             `json:"id"`      // 同一个消息里面的id是相同的
	Created  int64              `json:"created"` // 消息初始生成时间
	Content  string             `json:"content"` // 消息具体内容
	Document []*schema.Document `json:"document"`
}

// sseWriter wraps http.ResponseWriter for SSE writing with error tracking.
// GoFrame's Writeln/Flush return no error; we use RawWriter() to detect write failures.
type sseWriter struct {
	w      http.ResponseWriter
	failed bool
}

// writeEvent writes raw SSE event data and flushes. Returns error on write failure.
func (s *sseWriter) writeEvent(data string) error {
	if s.failed {
		return errWriteFailed
	}
	_, err := s.w.Write([]byte(data))
	if err != nil {
		s.failed = true
		return fmt.Errorf("sse write failed: %w", err)
	}
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// writeDocuments sends the documents event.
func (s *sseWriter) writeDocuments(data string) error {
	return s.writeEvent(fmt.Sprintf("documents:%s\n", data))
}

// writeData sends a data event with the given JSON payload.
func (s *sseWriter) writeData(jsonData string) error {
	return s.writeEvent(fmt.Sprintf("data:%s\n", jsonData))
}

// writeDone sends the [DONE] sentinel event.
func (s *sseWriter) writeDone() error {
	return s.writeEvent("data:[DONE]\n")
}

// writeError sends an error event. The error text is safely encoded (no line breaks).
func (s *sseWriter) writeError(err error) error {
	safeErr := strings.ReplaceAll(err.Error(), "\n", " ")
	safeErr = strings.ReplaceAll(safeErr, "\r", " ")
	return s.writeEvent(fmt.Sprintf("event: error\ndata: %s\n\n", safeErr))
}

// SteamResponse streams SSE responses from a StreamReader.
// Only normal EOF sends [DONE]; non-EOF errors send an error event without DONE.
func SteamResponse(ctx context.Context, streamReader *schema.StreamReader[*schema.Message], docs []*schema.Document) (err error) {
	httpReq := ghttp.RequestFromCtx(ctx)
	httpResp := httpReq.Response
	// 设置响应头
	httpResp.Header().Set("Content-Type", "text/event-stream")
	httpResp.Header().Set("Cache-Control", "no-cache")
	httpResp.Header().Set("Connection", "keep-alive")
	httpResp.Header().Set("X-Accel-Buffering", "no") // 禁用Nginx缓冲
	httpResp.Header().Set("Access-Control-Allow-Origin", "*")

	sw := &sseWriter{w: httpResp.RawWriter()}

	sd := &StreamData{
		Id:      uuid.NewString(),
		Created: time.Now().Unix(),
	}
	if len(docs) > 0 {
		sd.Document = docs
		marshal, marshalErr := sonic.Marshal(sd)
		if marshalErr != nil {
			return fmt.Errorf("encode documents failed: %w", marshalErr)
		}
		if writeErr := sw.writeDocuments(string(marshal)); writeErr != nil {
			return writeErr
		}
	}
	sd.Document = nil // 置空，发一次就够了

	// 处理流式响应
	for {
		chunk, recvErr := streamReader.Recv()
		if recvErr == io.EOF {
			// 正常结束才发 DONE
			if writeErr := sw.writeDone(); writeErr != nil {
				return writeErr
			}
			return nil
		}
		if recvErr != nil {
			// 非 EOF 错误：发送 error 事件，不发 DONE，返回带错误链的 error
			_ = sw.writeError(recvErr)
			return fmt.Errorf("stream read failed: %w", recvErr)
		}
		if chunk == nil {
			return fmt.Errorf("nil chunk received")
		}
		if len(chunk.Content) == 0 {
			continue
		}

		sd.Content = chunk.Content
		marshal, marshalErr := sonic.Marshal(sd)
		if marshalErr != nil {
			return fmt.Errorf("encode content failed: %w", marshalErr)
		}
		if writeErr := sw.writeData(string(marshal)); writeErr != nil {
			return writeErr
		}
	}
}

// streamResponseForTest is exported for testing only — accepts http.ResponseWriter directly.
// This avoids needing a full GoFrame context in tests.
func streamResponseForTest(w http.ResponseWriter, streamReader *schema.StreamReader[*schema.Message], docs []*schema.Document) error {
	sw := &sseWriter{w: w}

	sd := &StreamData{
		Id:      uuid.NewString(),
		Created: time.Now().Unix(),
	}
	if len(docs) > 0 {
		sd.Document = docs
		marshal, marshalErr := json.Marshal(sd)
		if marshalErr != nil {
			return fmt.Errorf("encode documents failed: %w", marshalErr)
		}
		if writeErr := sw.writeDocuments(string(marshal)); writeErr != nil {
			return writeErr
		}
	}
	sd.Document = nil

	for {
		chunk, recvErr := streamReader.Recv()
		if recvErr == io.EOF {
			if writeErr := sw.writeDone(); writeErr != nil {
				return writeErr
			}
			return nil
		}
		if recvErr != nil {
			_ = sw.writeError(recvErr)
			return fmt.Errorf("stream read failed: %w", recvErr)
		}
		if chunk == nil {
			return fmt.Errorf("nil chunk received")
		}
		if len(chunk.Content) == 0 {
			continue
		}

		sd.Content = chunk.Content
		marshal, marshalErr := json.Marshal(sd)
		if marshalErr != nil {
			return fmt.Errorf("encode content failed: %w", marshalErr)
		}
		if writeErr := sw.writeData(string(marshal)); writeErr != nil {
			return writeErr
		}
	}
}
