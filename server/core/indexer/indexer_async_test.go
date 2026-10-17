package indexer

import (
	"context"
	"testing"

	"github.com/cloudwego/eino-ext/components/indexer/es8"
	"github.com/cloudwego/eino/schema"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
)

func TestDocumentToFields_MissingQA(t *testing.T) {
	ctx := context.Background()
	doc := &schema.Document{
		ID:      "doc-1",
		Content: "test content",
		MetaData: map[string]any{
			coretypes.FieldExtra: "extra-data",
		},
	}

	fields, err := documentToFields(ctx, doc, "test-kb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 基础字段
	if f, ok := fields[coretypes.FieldContent]; !ok || f.Value != "test content" {
		t.Errorf("missing or wrong content field: %v", fields[coretypes.FieldContent])
	}
	if f, ok := fields[coretypes.KnowledgeName]; !ok || f.Value != "test-kb" {
		t.Errorf("missing or wrong knowledge name field: %v", fields[coretypes.KnowledgeName])
	}

	// QA 字段不应存在
	if _, ok := fields[coretypes.FieldQAContent]; ok {
		t.Error("QA field should not be present when missing")
	}
}

func TestDocumentToFields_EmptyQA(t *testing.T) {
	ctx := context.Background()
	doc := &schema.Document{
		ID:      "doc-1",
		Content: "test content",
		MetaData: map[string]any{
			coretypes.FieldQAContent: "", // 空 QA
		},
	}

	fields, err := documentToFields(ctx, doc, "test-kb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 空 QA 不应写入
	if _, ok := fields[coretypes.FieldQAContent]; ok {
		t.Error("empty QA field should not be present")
	}
}

func TestDocumentToFields_NonEmptyQA(t *testing.T) {
	ctx := context.Background()
	doc := &schema.Document{
		ID:      "doc-1",
		Content: "test content",
		MetaData: map[string]any{
			coretypes.FieldQAContent: "What is this?",
		},
	}

	fields, err := documentToFields(ctx, doc, "test-kb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// QA 字段应存在且有 EmbedKey
	f, ok := fields[coretypes.FieldQAContent]
	if !ok {
		t.Fatal("QA field should be present")
	}
	if f.Value != "What is this?" {
		t.Errorf("QA value = %q, want 'What is this?'", f.Value)
	}
	if f.EmbedKey != coretypes.FieldQAContentVector {
		t.Errorf("QA EmbedKey = %q, want %q", f.EmbedKey, coretypes.FieldQAContentVector)
	}
}

func TestDocumentToFields_DoesNotModifyInput(t *testing.T) {
	ctx := context.Background()
	doc := &schema.Document{
		ID:      "doc-1",
		Content: "test content",
		MetaData: map[string]any{
			"key": "value",
		},
	}

	// 记录输入原始状态
	origLen := len(doc.MetaData)
	origKeys := make(map[string]struct{})
	for k := range doc.MetaData {
		origKeys[k] = struct{}{}
	}

	_, err := documentToFields(ctx, doc, "test-kb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 输入 doc 不应被修改
	if len(doc.MetaData) != origLen {
		t.Errorf("MetaData length changed: %d -> %d", origLen, len(doc.MetaData))
	}
	for k := range doc.MetaData {
		if _, ok := origKeys[k]; !ok {
			t.Errorf("new key %q added to MetaData", k)
		}
	}
}

func TestDocumentToFields_WithExtra(t *testing.T) {
	ctx := context.Background()
	doc := &schema.Document{
		ID:      "doc-1",
		Content: "test content",
		MetaData: map[string]any{
			coretypes.FieldExtra: "serialized-extra",
		},
	}

	fields, err := documentToFields(ctx, doc, "test-kb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Extra 字段应存在
	f, ok := fields[coretypes.FieldExtra]
	if !ok {
		t.Fatal("Extra field should be present")
	}
	if f.Value != "serialized-extra" {
		t.Errorf("Extra value = %v, want 'serialized-extra'", f.Value)
	}
}

func TestDocumentToFields_ContentEmbedKey(t *testing.T) {
	ctx := context.Background()
	doc := &schema.Document{
		ID:       "doc-1",
		Content:  "test content",
		MetaData: map[string]any{},
	}

	fields, err := documentToFields(ctx, doc, "test-kb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Content 字段应有 EmbedKey
	f := fields[coretypes.FieldContent]
	if f.EmbedKey != coretypes.FieldContentVector {
		t.Errorf("Content EmbedKey = %q, want %q", f.EmbedKey, coretypes.FieldContentVector)
	}
}

// 确保 fields map 是新的，不共享引用
func TestDocumentToFields_ReturnsNewMap(t *testing.T) {
	ctx := context.Background()
	doc := &schema.Document{
		ID:       "doc-1",
		Content:  "test content",
		MetaData: map[string]any{},
	}

	fields1, _ := documentToFields(ctx, doc, "test-kb")
	fields2, _ := documentToFields(ctx, doc, "test-kb")

	// 修改 fields1 不应影响 fields2
	fields1["new-key"] = es8.FieldValue{Value: "new"}
	if _, ok := fields2["new-key"]; ok {
		t.Error("fields maps should be independent")
	}
}
