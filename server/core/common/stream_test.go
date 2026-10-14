package common

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
)

// fakeSSEWriter records SSE events for testing.
type fakeSSEWriter struct {
	events     []fakeEvent
	writeErr   error // if set, writes fail with this error
	failOnCall int   // fail when writeCount > failOnCall (0 = fail immediately)
	writeCount int
}

type fakeEvent struct {
	event string
	data  string
}

func (f *fakeSSEWriter) writeDocuments(data string) error {
	return f.record("documents", data)
}

func (f *fakeSSEWriter) writeData(jsonData string) error {
	return f.record("data", jsonData)
}

func (f *fakeSSEWriter) writeDone() error {
	return f.record("data", "[DONE]")
}

func (f *fakeSSEWriter) writeError(err error) error {
	safeErr := strings.ReplaceAll(err.Error(), "\n", " ")
	safeErr = strings.ReplaceAll(safeErr, "\r", " ")
	return f.record("error", safeErr)
}

func (f *fakeSSEWriter) record(event, data string) error {
	f.writeCount++
	if f.writeErr != nil && f.writeCount > f.failOnCall {
		return f.writeErr
	}
	f.events = append(f.events, fakeEvent{event: event, data: data})
	return nil
}

func msg(content string) *schema.Message {
	return &schema.Message{Role: schema.Assistant, Content: content}
}

// --- Normal EOF tests ---

func TestStreamResponse_NormalEOF(t *testing.T) {
	sr := schema.StreamReaderFromArray([]*schema.Message{
		msg("hello"),
		msg(" world"),
	})
	defer sr.Close()

	fw := &fakeSSEWriter{}
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, nil, enc)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	// Should have 3 events: data, data, done
	if len(fw.events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(fw.events))
	}
	if fw.events[0].event != "data" || !strings.Contains(fw.events[0].data, "hello") {
		t.Errorf("event 0: expected data with 'hello', got %+v", fw.events[0])
	}
	if fw.events[1].event != "data" || !strings.Contains(fw.events[1].data, "world") {
		t.Errorf("event 1: expected data with 'world', got %+v", fw.events[1])
	}
	if fw.events[2].event != "data" || fw.events[2].data != "[DONE]" {
		t.Errorf("event 2: expected data [DONE], got %+v", fw.events[2])
	}
}

func TestStreamResponse_EmptyStream(t *testing.T) {
	sr := schema.StreamReaderFromArray([]*schema.Message{})
	defer sr.Close()

	fw := &fakeSSEWriter{}
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, nil, enc)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	// Should only have DONE event
	if len(fw.events) != 1 {
		t.Fatalf("expected 1 event (DONE), got %d", len(fw.events))
	}
	if fw.events[0].data != "[DONE]" {
		t.Errorf("expected [DONE], got %+v", fw.events[0])
	}
}

func TestStreamResponse_EmptyContentChunks(t *testing.T) {
	sr := schema.StreamReaderFromArray([]*schema.Message{
		msg(""), // empty content → skipped
		msg("visible"),
		msg(""), // another empty → skipped
	})
	defer sr.Close()

	fw := &fakeSSEWriter{}
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, nil, enc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have 2 events: data (visible), done
	if len(fw.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(fw.events))
	}
	if !strings.Contains(fw.events[0].data, "visible") {
		t.Errorf("expected 'visible' in data, got %+v", fw.events[0])
	}
}

// --- Stream error tests ---

func TestStreamResponse_StreamError_NoDone(t *testing.T) {
	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		sw.Send(msg("partial"), nil)
		sw.Send(nil, errors.New("model error"))
		sw.Close()
	}()
	defer sr.Close()

	fw := &fakeSSEWriter{}
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, nil, enc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "model error") {
		t.Errorf("expected 'model error' in error, got: %v", err)
	}

	// Should have: data (partial), error event, NO done
	if len(fw.events) != 2 {
		t.Fatalf("expected 2 events (data + error), got %d", len(fw.events))
	}
	if fw.events[0].event != "data" || !strings.Contains(fw.events[0].data, "partial") {
		t.Errorf("event 0: expected data with 'partial', got %+v", fw.events[0])
	}
	if fw.events[1].event != "error" || !strings.Contains(fw.events[1].data, "model error") {
		t.Errorf("event 1: expected error event, got %+v", fw.events[1])
	}
}

// --- nil chunk test ---

func TestStreamResponse_NilChunk(t *testing.T) {
	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		sw.Send(nil, nil) // nil chunk, no error
		sw.Close()
	}()
	defer sr.Close()

	fw := &fakeSSEWriter{}
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, nil, enc)
	if err == nil {
		t.Fatal("expected error for nil chunk")
	}
	if !strings.Contains(err.Error(), "nil chunk") {
		t.Errorf("expected 'nil chunk' in error, got: %v", err)
	}
}

// --- Error chain tests ---

func TestStreamResponse_ErrorChain(t *testing.T) {
	originalErr := errors.New("upstream_error")
	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		sw.Send(nil, originalErr)
		sw.Close()
	}()
	defer sr.Close()

	fw := &fakeSSEWriter{}
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, nil, enc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, originalErr) {
		t.Errorf("error should wrap original: errors.Is failed, got: %v", err)
	}
}

// --- Safe error encoding tests ---

func TestStreamResponse_SafeErrorEncoding(t *testing.T) {
	errWithNewlines := errors.New("error\nwith\nnewlines\r\nand\rcarriage_returns")
	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		sw.Send(nil, errWithNewlines)
		sw.Close()
	}()
	defer sr.Close()

	fw := &fakeSSEWriter{}
	enc := sonic.ConfigDefault
	_ = streamResponse(fw, sr, nil, enc)

	// Should have error event
	if len(fw.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(fw.events))
	}
	if fw.events[0].event != "error" {
		t.Errorf("expected error event, got %+v", fw.events[0])
	}
	// Newlines should be replaced with spaces
	if strings.Contains(fw.events[0].data, "\n") || strings.Contains(fw.events[0].data, "\r") {
		t.Error("error data should not contain raw newlines")
	}
	if !strings.Contains(fw.events[0].data, "error with newlines") {
		t.Errorf("expected spaces instead of newlines, got: %s", fw.events[0].data)
	}
}

// --- Write failure tests ---

func TestStreamResponse_WriteFail_Documents(t *testing.T) {
	docs := []*schema.Document{{Content: "doc1"}}
	sr := schema.StreamReaderFromArray([]*schema.Message{msg("hello")})
	defer sr.Close()

	writeErr := errors.New("simulated write failure")
	fw := &fakeSSEWriter{writeErr: writeErr, failOnCall: 0}
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, docs, enc)
	if err == nil {
		t.Fatal("expected write error for documents")
	}
	if !errors.Is(err, writeErr) {
		t.Errorf("expected write error, got: %v", err)
	}
}

func TestStreamResponse_WriteFail_Data(t *testing.T) {
	sr := schema.StreamReaderFromArray([]*schema.Message{msg("hello")})
	defer sr.Close()

	writeErr := errors.New("simulated write failure")
	fw := &fakeSSEWriter{writeErr: writeErr, failOnCall: 0}
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, nil, enc)
	if err == nil {
		t.Fatal("expected write error for data")
	}
	if !errors.Is(err, writeErr) {
		t.Errorf("expected write error, got: %v", err)
	}
}

func TestStreamResponse_WriteFail_Done(t *testing.T) {
	sr := schema.StreamReaderFromArray([]*schema.Message{msg("hi")})
	defer sr.Close()

	writeErr := errors.New("simulated write failure")
	fw := &fakeSSEWriter{writeErr: writeErr, failOnCall: 1} // first write succeeds
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, nil, enc)
	if err == nil {
		t.Fatal("expected write error for DONE")
	}
	if !errors.Is(err, writeErr) {
		t.Errorf("expected write error, got: %v", err)
	}
}

func TestStreamResponse_ErrorEventWriteFailure(t *testing.T) {
	readErr := errors.New("read error")
	writeErr := errors.New("write error")

	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		sw.Send(msg("partial"), nil)
		sw.Send(nil, readErr)
		sw.Close()
	}()
	defer sr.Close()

	// failOnCall:1 → first write (data "partial") succeeds, second write (error event) fails
	fw := &fakeSSEWriter{writeErr: writeErr, failOnCall: 1}
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, nil, enc)

	// errors.Is should recognize both read and write sentinels
	if !errors.Is(err, readErr) {
		t.Errorf("should recognize read error: %v", err)
	}
	if !errors.Is(err, writeErr) {
		t.Errorf("should recognize write error: %v", err)
	}
}

func TestStreamResponse_NoContinueAfterWriteFailure(t *testing.T) {
	// After a write failure, streamResponse should stop immediately
	sr := schema.StreamReaderFromArray([]*schema.Message{
		msg("first"),
		msg("second"),
		msg("third"),
	})
	defer sr.Close()

	writeErr := errors.New("write failed")
	fw := &fakeSSEWriter{writeErr: writeErr, failOnCall: 1} // first succeeds, second fails
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, nil, enc)

	if err == nil {
		t.Fatal("expected error")
	}
	// Should have only 1 event (first data), then stopped
	if len(fw.events) != 1 {
		t.Errorf("expected 1 event before failure, got %d", len(fw.events))
	}
}

// --- Documents tests ---

func TestStreamResponse_WithDocuments(t *testing.T) {
	docs := []*schema.Document{
		{Content: "doc1"},
		{Content: "doc2"},
	}
	sr := schema.StreamReaderFromArray([]*schema.Message{msg("answer")})
	defer sr.Close()

	fw := &fakeSSEWriter{}
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, docs, enc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have: documents, data, done
	if len(fw.events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(fw.events))
	}
	if fw.events[0].event != "documents" || !strings.Contains(fw.events[0].data, "doc1") {
		t.Errorf("event 0: expected documents with 'doc1', got %+v", fw.events[0])
	}
	if fw.events[1].event != "data" || !strings.Contains(fw.events[1].data, "answer") {
		t.Errorf("event 1: expected data with 'answer', got %+v", fw.events[1])
	}
	if fw.events[2].data != "[DONE]" {
		t.Errorf("event 2: expected [DONE], got %+v", fw.events[2])
	}
}

func TestStreamResponse_DocumentsEncodeFailure(t *testing.T) {
	// Use real un-encodable metadata (function cannot be JSON encoded)
	docs := []*schema.Document{
		{
			Content: "doc1",
			MetaData: map[string]any{
				"invalid": func() {}, // function cannot be JSON encoded
			},
		},
	}
	sr := schema.StreamReaderFromArray([]*schema.Message{msg("answer")})
	defer sr.Close()

	fw := &fakeSSEWriter{}
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, docs, enc)
	if err == nil {
		t.Fatal("expected encode error")
	}
	if !strings.Contains(err.Error(), "encode documents failed") {
		t.Errorf("expected 'encode documents failed', got: %v", err)
	}
	// Should not have written any events
	if len(fw.events) != 0 {
		t.Errorf("expected 0 events after encode failure, got %d", len(fw.events))
	}
}

// --- SSE event format tests ---

func TestStreamResponse_EventFrameBoundary(t *testing.T) {
	// Verify that events are properly structured
	sr := schema.StreamReaderFromArray([]*schema.Message{msg("test")})
	defer sr.Close()

	fw := &fakeSSEWriter{}
	enc := sonic.ConfigDefault
	err := streamResponse(fw, sr, nil, enc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify event structure
	if len(fw.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(fw.events))
	}

	// Data event
	if fw.events[0].event != "data" {
		t.Errorf("event 0: expected 'data' event type, got %q", fw.events[0].event)
	}
	if !strings.Contains(fw.events[0].data, "test") {
		t.Errorf("event 0: expected 'test' in data, got %q", fw.events[0].data)
	}

	// DONE event
	if fw.events[1].event != "data" {
		t.Errorf("event 1: expected 'data' event type, got %q", fw.events[1].event)
	}
	if fw.events[1].data != "[DONE]" {
		t.Errorf("event 1: expected '[DONE]', got %q", fw.events[1].data)
	}
}

func TestStreamResponse_ErrorEventFormat(t *testing.T) {
	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		sw.Send(nil, errors.New("test error"))
		sw.Close()
	}()
	defer sr.Close()

	fw := &fakeSSEWriter{}
	enc := sonic.ConfigDefault
	_ = streamResponse(fw, sr, nil, enc)

	if len(fw.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(fw.events))
	}
	if fw.events[0].event != "error" {
		t.Errorf("expected 'error' event type, got %q", fw.events[0].event)
	}
	if !strings.Contains(fw.events[0].data, "test error") {
		t.Errorf("expected 'test error' in data, got %q", fw.events[0].data)
	}
}

// --- Short write and flush tests ---

func TestRealSSEWriter_ShortWrite(t *testing.T) {
	// Simulate short write
	rec := &shortWriteRecorder{maxBytes: 5}
	sw := &realSSEWriter{w: rec}

	err := sw.writeData("test")
	if !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("expected io.ErrShortWrite, got: %v", err)
	}
	if !sw.failed {
		t.Error("expected failed flag to be set")
	}
}

func TestRealSSEWriter_WriteFailure(t *testing.T) {
	rec := &failingHTTPWriter{failOnCall: 0}
	sw := &realSSEWriter{w: rec}

	err := sw.writeData("test")
	if err == nil {
		t.Fatal("expected write error")
	}
	if !sw.failed {
		t.Error("expected failed flag to be set")
	}
	// Subsequent writes should return errWriteFailed
	err = sw.writeDone()
	if !errors.Is(err, errWriteFailed) {
		t.Errorf("expected errWriteFailed on subsequent write, got: %v", err)
	}
}

// shortWriteRecorder simulates short writes.
type shortWriteRecorder struct {
	maxBytes int
}

func (s *shortWriteRecorder) Write(b []byte) (int, error) {
	if len(b) > s.maxBytes {
		return s.maxBytes, nil // short write
	}
	return len(b), nil
}

func (s *shortWriteRecorder) Header() http.Header {
	return make(http.Header)
}

func (s *shortWriteRecorder) WriteHeader(statusCode int) {}

func (s *shortWriteRecorder) Flush() {}

// failingHTTPWriter simulates write failures.
type failingHTTPWriter struct {
	failOnCall int
	callCount  int
}

func (f *failingHTTPWriter) Write(b []byte) (int, error) {
	f.callCount++
	if f.callCount > f.failOnCall {
		return 0, errors.New("simulated write failure")
	}
	return len(b), nil
}

func (f *failingHTTPWriter) Header() http.Header {
	return make(http.Header)
}

func (f *failingHTTPWriter) WriteHeader(statusCode int) {}

func (f *failingHTTPWriter) Flush() {}
