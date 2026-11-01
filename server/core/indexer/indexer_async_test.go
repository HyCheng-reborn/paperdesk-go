package indexer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino-ext/components/indexer/es8"
	"github.com/cloudwego/eino/schema"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
)

// 所有测试都调用生产回调委派使用的同一 helper：documentToFields。

func TestDocumentToFields_BasicFieldsAndExtra(t *testing.T) {
	doc := &schema.Document{
		ID:      "doc-1",
		Content: "test content",
		MetaData: map[string]any{
			"_file_name": "report.pdf", // 真实 ExtKeys 成员，getExtData 会保留
		},
	}

	fields, err := documentToFields(context.Background(), doc, "test-kb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if f := fields[coretypes.FieldContent]; f.Value != "test content" || f.EmbedKey != coretypes.FieldContentVector {
		t.Errorf("content field wrong: %+v", f)
	}
	if f := fields[coretypes.KnowledgeName]; f.Value != "test-kb" {
		t.Errorf("knowledge field wrong: %+v", f)
	}

	// Extra 来自 getExtData 的序列化，且应包含真实 ext 数据
	raw, ok := fields[coretypes.FieldExtra].Value.(string)
	if !ok {
		t.Fatalf("extra field not a string: %#v", fields[coretypes.FieldExtra].Value)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("extra field is not valid JSON: %v (%q)", err, raw)
	}
	if decoded["_file_name"] != "report.pdf" {
		t.Errorf("extra JSON missing _file_name: %v", decoded)
	}
}

func TestDocumentToFields_QAEmptyAndNonEmpty(t *testing.T) {
	// 空 QA 不写入
	empty := &schema.Document{ID: "d", Content: "c", MetaData: map[string]any{coretypes.FieldQAContent: ""}}
	f1, err := documentToFields(context.Background(), empty, "kb")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if _, ok := f1[coretypes.FieldQAContent]; ok {
		t.Error("empty QA field must not be present")
	}

	// 非空 QA 写入并带 EmbedKey
	full := &schema.Document{ID: "d", Content: "c", MetaData: map[string]any{coretypes.FieldQAContent: "What?"}}
	f2, err := documentToFields(context.Background(), full, "kb")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	qa := f2[coretypes.FieldQAContent]
	if qa.Value != "What?" || qa.EmbedKey != coretypes.FieldQAContentVector {
		t.Errorf("QA field wrong: %+v", qa)
	}
}

func TestDocumentToFields_QAMissing(t *testing.T) {
	doc := &schema.Document{ID: "d", Content: "c", MetaData: map[string]any{"_source": "url"}}
	fields, err := documentToFields(context.Background(), doc, "kb")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if _, ok := fields[coretypes.FieldQAContent]; ok {
		t.Error("QA field should be absent when missing")
	}
}

func TestDocumentToFields_DoesNotModifyInput(t *testing.T) {
	doc := &schema.Document{
		ID:      "doc-1",
		Content: "c",
		MetaData: map[string]any{
			"_file_name": "a.txt",
		},
	}
	origKeys := map[string]any{"_file_name": "a.txt"}

	if _, err := documentToFields(context.Background(), doc, "kb"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	// 输入 MetaData 不应被写入任何新键（尤其不得出现 FieldExtra）
	if _, ok := doc.MetaData[coretypes.FieldExtra]; ok {
		t.Error("helper must not write FieldExtra back into input MetaData")
	}
	if len(doc.MetaData) != len(origKeys) {
		t.Errorf("input MetaData length changed: %v", doc.MetaData)
	}
	if doc.MetaData["_file_name"] != "a.txt" {
		t.Errorf("input ext value changed: %v", doc.MetaData["_file_name"])
	}
	// Content 不得被修改
	if doc.Content != "c" || doc.ID != "doc-1" {
		t.Errorf("input doc identity/content changed: id=%q content=%q", doc.ID, doc.Content)
	}
}

func TestDocumentToFields_ReturnsIndependentMap(t *testing.T) {
	doc := &schema.Document{ID: "d", Content: "c", MetaData: map[string]any{"_file_name": "x"}}
	f1, _ := documentToFields(context.Background(), doc, "kb")
	f2, _ := documentToFields(context.Background(), doc, "kb")
	f1["probe"] = es8.FieldValue{Value: "v"}
	if _, ok := f2["probe"]; ok {
		t.Error("returned maps must be independent")
	}
}

// TestDocumentToFields_MarshalError 用 getExtData 实际保留的不可编码值（ExtKeys 成员下的 func）
// 触发真实 sonic.Marshal 失败：必须返回可追踪错误且不返回成功字段。
func TestDocumentToFields_MarshalError(t *testing.T) {
	doc := &schema.Document{
		ID:      "doc-bad",
		Content: "c",
		MetaData: map[string]any{
			"_file_name": func() {}, // 属于 ExtKeys，getExtData 会保留 → 序列化必然失败
		},
	}

	fields, err := documentToFields(context.Background(), doc, "kb")
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if !strings.Contains(err.Error(), "marshal ext") || !strings.Contains(err.Error(), "doc-bad") {
		t.Fatalf("error should be a traceable marshal-ext error naming the doc, got: %v", err)
	}
	if fields != nil {
		t.Fatalf("on marshal failure no fields should be returned, got: %v", fields)
	}
}

// TestDocumentToFields_NilMetaDataNoExtra：MetaData 为 nil 时不写 Extra 字段，也不应 panic。
func TestDocumentToFields_NilMetaDataNoExtra(t *testing.T) {
	doc := &schema.Document{ID: "d", Content: "c", MetaData: nil}
	fields, err := documentToFields(context.Background(), doc, "kb")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if _, ok := fields[coretypes.FieldExtra]; ok {
		t.Error("no Extra expected when MetaData is nil")
	}
	if fields[coretypes.FieldContent].EmbedKey != coretypes.FieldContentVector {
		t.Error("content EmbedKey should be preserved")
	}
}
