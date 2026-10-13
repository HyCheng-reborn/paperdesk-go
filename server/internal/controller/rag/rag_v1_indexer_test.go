package rag

import (
	"mime/multipart"
	"strings"
	"testing"

	"github.com/gogf/gf/v2/net/ghttp"

	v1 "github.com/wangle201210/go-rag/server/api/rag/v1"
)

// newUploadFile creates a ghttp.UploadFile with the given filename for testing.
func newUploadFile(filename string) *ghttp.UploadFile {
	return &ghttp.UploadFile{
		FileHeader: &multipart.FileHeader{Filename: filename},
	}
}

// --- validateIndexerInput tests ---

func TestValidateIndexerInput_NoFileNoURL(t *testing.T) {
	req := &v1.IndexerReq{}
	err := validateIndexerInput(req)
	if err == nil {
		t.Fatal("expected error when neither File nor URL is provided")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected 'required' in error message, got: %v", err)
	}
}

func TestValidateIndexerInput_FileOnly(t *testing.T) {
	req := &v1.IndexerReq{
		File: newUploadFile("test.pdf"),
	}
	err := validateIndexerInput(req)
	if err != nil {
		t.Fatalf("expected no error for file-only input, got: %v", err)
	}
}

func TestValidateIndexerInput_URLOnly_DefaultMode(t *testing.T) {
	// Default mode (PaperDesk off): URL-only is accepted.
	paperDeskModeOverride = nil
	defer func() { paperDeskModeOverride = nil }()

	req := &v1.IndexerReq{URL: "http://example.com/doc.pdf"}
	err := validateIndexerInput(req)
	if err != nil {
		t.Fatalf("expected no error for URL-only input in default mode, got: %v", err)
	}
}

func TestValidateIndexerInput_FileAndURL_DefaultMode(t *testing.T) {
	// Default mode (PaperDesk off): both File and URL is accepted.
	paperDeskModeOverride = nil
	defer func() { paperDeskModeOverride = nil }()

	req := &v1.IndexerReq{
		File: newUploadFile("test.pdf"),
		URL:  "http://example.com/doc.pdf",
	}
	err := validateIndexerInput(req)
	if err != nil {
		t.Fatalf("expected no error for file+URL input in default mode, got: %v", err)
	}
}

func TestValidateIndexerInput_URLOnly_PaperDeskMode(t *testing.T) {
	paperDeskModeOverride = func() bool { return true }
	defer func() { paperDeskModeOverride = nil }()

	req := &v1.IndexerReq{URL: "http://example.com/doc.pdf"}
	err := validateIndexerInput(req)
	if err == nil {
		t.Fatal("expected error for URL-only input in PaperDesk mode")
	}
	if !strings.Contains(err.Error(), "PaperDesk") {
		t.Fatalf("expected 'PaperDesk' in error message, got: %v", err)
	}
}

func TestValidateIndexerInput_FileAndURL_PaperDeskMode(t *testing.T) {
	paperDeskModeOverride = func() bool { return true }
	defer func() { paperDeskModeOverride = nil }()

	req := &v1.IndexerReq{
		File: newUploadFile("test.pdf"),
		URL:  "http://example.com/doc.pdf",
	}
	err := validateIndexerInput(req)
	if err == nil {
		t.Fatal("expected error for file+URL input in PaperDesk mode")
	}
	if !strings.Contains(err.Error(), "PaperDesk") {
		t.Fatalf("expected 'PaperDesk' in error message, got: %v", err)
	}
}

func TestValidateIndexerInput_FileOnly_PaperDeskMode(t *testing.T) {
	paperDeskModeOverride = func() bool { return true }
	defer func() { paperDeskModeOverride = nil }()

	req := &v1.IndexerReq{
		File: newUploadFile("test.pdf"),
	}
	err := validateIndexerInput(req)
	if err != nil {
		t.Fatalf("expected no error for file-only input in PaperDesk mode, got: %v", err)
	}
}

func TestValidateIndexerInput_EmptyFilenameTreatedAsNoFile(t *testing.T) {
	// File pointer non-nil but Filename empty → treated as no file.
	req := &v1.IndexerReq{
		File: newUploadFile(""),
	}
	err := validateIndexerInput(req)
	if err == nil {
		t.Fatal("expected error when File has empty Filename and no URL")
	}
}

func TestValidateIndexerInput_WhitespaceURLTreatedAsNoURL(t *testing.T) {
	req := &v1.IndexerReq{URL: "   "}
	err := validateIndexerInput(req)
	if err == nil {
		t.Fatal("expected error when URL is whitespace-only and no File")
	}
}

// --- Indexer no-side-effects test ---

func TestIndexer_NoSideEffects_OnValidationError(t *testing.T) {
	// When validation fails, Indexer must return error immediately
	// without calling rag.GetRagSvr(), saving files, writing DB, or starting tasks.
	// We verify this by confirming the error is the validation error,
	// not a panic or a downstream error.
	c := &ControllerV1{}
	req := &v1.IndexerReq{} // no File, no URL → validation error

	res, err := c.Indexer(nil, req)
	if err == nil {
		t.Fatal("expected error from Indexer with empty request")
	}
	if res != nil {
		t.Fatal("expected nil response from Indexer with empty request")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestIndexer_NoSideEffects_OnValidationError_PaperDeskMode(t *testing.T) {
	paperDeskModeOverride = func() bool { return true }
	defer func() { paperDeskModeOverride = nil }()

	c := &ControllerV1{}
	req := &v1.IndexerReq{URL: "http://example.com/doc.pdf"}

	res, err := c.Indexer(nil, req)
	if err == nil {
		t.Fatal("expected error from Indexer with URL-only in PaperDesk mode")
	}
	if res != nil {
		t.Fatal("expected nil response from Indexer with URL-only in PaperDesk mode")
	}
	if !strings.Contains(err.Error(), "PaperDesk") {
		t.Fatalf("expected PaperDesk validation error, got: %v", err)
	}
}
