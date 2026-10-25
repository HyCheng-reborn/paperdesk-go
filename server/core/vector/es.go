package vector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/cenkalti/backoff/v4"
	"github.com/cloudwego/eino/schema"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/typedapi/core/search"
	"github.com/elastic/go-elasticsearch/v8/typedapi/indices/create"
	"github.com/elastic/go-elasticsearch/v8/typedapi/indices/exists"
	"github.com/elastic/go-elasticsearch/v8/typedapi/types"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/wangle201210/go-rag/server/core/config"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
)

// ESVectorStore Elasticsearch 向量存储实现
type ESVectorStore struct {
	dimensions int
	client     *elasticsearch.Client
}

// NewESVectorStore 创建 ES 向量存储实例
func NewESVectorStore(cfg *ESConfig, dimensions ...int) (*ESVectorStore, error) {
	d, err := storeDimensions(dimensions)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("nil vector configuration")
	}
	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{cfg.Address},
		Username:  cfg.Username,
		Password:  cfg.Password,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create es client: %w", err)
	}

	return &ESVectorStore{
		client:     client,
		dimensions: d,
	}, nil
}

// GetClient 获取 ES 客户端（用于兼容现有代码）
func (s *ESVectorStore) GetClient() *elasticsearch.Client {
	return s.client
}

// CreateIndex 创建索引，原子携带 fingerprint 到 mappings._meta（C2/C3）。
// fp 为 nil 时仅创建 schema（向后兼容）。
// 创建成功后回读真实 UUID 并写入 _meta.go-rag-resource-id，再验证 schema 维度。
// 若索引已存在返回 ErrIndexAlreadyExists；任何回读步骤失败则删除索引并返回错误。
func (s *ESVectorStore) CreateIndex(ctx context.Context, indexName string, fp *Fingerprint) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// 构建 mapping（使用 fp.Dimensions 或 s.dimensions 回退）
	dims := s.dimensions
	if fp != nil && fp.Dimensions > 0 {
		dims = fp.Dimensions
	}
	mapping, err := config.ESMapping(dims)
	if err != nil {
		return err
	}

	// C2/C3: 若提供 fingerprint，原子写入 _meta
	if fp != nil {
		fpJSON, err := fp.MarshalJSON()
		if err != nil {
			return fmt.Errorf("marshal fingerprint: %w", err)
		}
		mapping.Meta_ = map[string]json.RawMessage{
			"go-rag-fingerprint": json.RawMessage(fpJSON),
		}
	}

	// 使用 typed API 创建索引
	_, err = create.NewCreateFunc(s.client)(indexName).Request(&create.Request{Mappings: mapping}).Do(ctx)
	if err != nil {
		// C3: 检测 resource_already_exists，返回特定 sentinel 让调用方走验证路径
		if isESAlreadyExists(err) {
			return ErrIndexAlreadyExists
		}
		return fmt.Errorf("create index: %w", err)
	}

	// fp == nil 时无需回读验证（向后兼容路径）
	if fp == nil {
		return nil
	}

	// 创建成功后回读验证（C2/C4）
	// 1. 读取 ES 分配的真实 UUID
	actualUUID, uuidErr := s.ReadESIndexUUID(ctx, indexName)
	if uuidErr != nil {
		s.deleteIndexBestEffort(ctx, indexName)
		return fmt.Errorf("read back index UUID after creation: %w", uuidErr)
	}
	if actualUUID == "" {
		s.deleteIndexBestEffort(ctx, indexName)
		return fmt.Errorf("ES returned empty index UUID for %q", indexName)
	}

	// 2. 用 PutMapping 补充 _meta.go-rag-resource-id 为真实 UUID
	metaBody := fmt.Sprintf(`{"_meta":{"go-rag-fingerprint":%s,"go-rag-resource-id":%s}}`,
		mustJSON(fp), mustJSONString(actualUUID))
	res, putErr := s.client.Indices.PutMapping(
		[]string{indexName},
		strings.NewReader(metaBody),
		s.client.Indices.PutMapping.WithContext(ctx),
	)
	if putErr != nil {
		s.deleteIndexBestEffort(ctx, indexName)
		return fmt.Errorf("put mapping for resource-id: %w", putErr)
	}
	res.Body.Close()
	if res.IsError() {
		s.deleteIndexBestEffort(ctx, indexName)
		return fmt.Errorf("put mapping for resource-id failed: %s", res.String())
	}

	// 3. 回读 mapping 验证 content_vector 和 qa_content_vector 实际维度
	if verifyErr := s.verifyESSchemaDimensions(ctx, indexName, fp.Dimensions); verifyErr != nil {
		s.deleteIndexBestEffort(ctx, indexName)
		return fmt.Errorf("schema verification after creation: %w", verifyErr)
	}

	return nil
}

// isESAlreadyExists 检测 ES 错误响应是否为 resource_already_exists。
func isESAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "resource_already_exists") ||
		strings.Contains(msg, "resource_already_exists_exception")
}

// deleteIndexBestEffort 尽力删除索引（失败仅日志记录）。
func (s *ESVectorStore) deleteIndexBestEffort(ctx context.Context, indexName string) {
	res, err := s.client.Indices.Delete(
		[]string{indexName},
		s.client.Indices.Delete.WithContext(ctx),
	)
	if err != nil {
		g.Log().Errorf(ctx, "best-effort delete of index %q failed: %v", indexName, err)
		return
	}
	res.Body.Close()
	if res.IsError() {
		g.Log().Errorf(ctx, "best-effort delete of index %q failed: %s", indexName, res.String())
	}
}

// verifyESSchemaDimensions 回读 ES mapping 验证 content_vector 和 qa_content_vector 维度。
func (s *ESVectorStore) verifyESSchemaDimensions(ctx context.Context, indexName string, expectedDims int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	res, err := s.client.Indices.Get(
		[]string{indexName},
		s.client.Indices.Get.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("get index for schema verification: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("get index for schema verification failed: %s", res.String())
	}
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read schema verification response: %w", err)
	}
	// 解析 mapping properties 以提取实际维度
	var result map[string]struct {
		Mappings struct {
			Properties map[string]map[string]any `json:"properties"`
		} `json:"mappings"`
	}
	if err := sonic.Unmarshal(bodyBytes, &result); err != nil {
		return fmt.Errorf("unmarshal schema verification response: %w", err)
	}
	state, ok := result[indexName]
	if !ok {
		return fmt.Errorf("index %q not found in schema verification response", indexName)
	}
	for _, field := range []string{coretypes.FieldContentVector, coretypes.FieldQAContentVector} {
		prop, fieldOK := state.Mappings.Properties[field]
		if !fieldOK {
			return fmt.Errorf("field %q missing from mapping properties", field)
		}
		dimsVal, dimsOK := prop["dims"]
		if !dimsOK {
			return fmt.Errorf("field %q missing dims in mapping", field)
		}
		// JSON number 解析为 float64
		actualDims, ok := dimsVal.(float64)
		if !ok {
			return fmt.Errorf("field %q dims is not a number: %v", field, dimsVal)
		}
		if int(actualDims) != expectedDims {
			return fmt.Errorf("%w: field %q expected dims=%d, got dims=%d",
				ErrFingerprintMismatch, field, expectedDims, int(actualDims))
		}
	}
	return nil
}

// mustJSON 将值序列化为 JSON 字符串（用于构造 PutMapping body）。
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustJSON: %v", err))
	}
	return string(b)
}

// mustJSONString 将字符串序列化为 JSON 字符串（带引号）。
func mustJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("mustJSONString: %v", err))
	}
	return string(b)
}

// IndexExists 检查索引是否存在
// F8: context propagation.
func (s *ESVectorStore) IndexExists(ctx context.Context, indexName string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return exists.NewExistsFunc(s.client)(indexName).Do(ctx)
}

// DeleteDocument 删除文档
func (s *ESVectorStore) DeleteDocument(ctx context.Context, indexName, documentID string) error {
	return s.withRetry(func() error {
		res, err := s.client.Delete(indexName, documentID)
		if err != nil {
			return fmt.Errorf("delete document failed: %w", err)
		}
		defer res.Body.Close()

		if res.IsError() {
			return fmt.Errorf("delete document failed: %s", res.String())
		}

		return nil
	})
}

// GetKnowledgeBaseList 获取知识库列表
func (s *ESVectorStore) GetKnowledgeBaseList(ctx context.Context, indexName string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	names := "distinct_knowledge_names"
	query := search.NewRequest()
	query.Size = of(0)
	query.Aggregations = map[string]types.Aggregations{
		names: {
			Terms: &types.TermsAggregation{
				Field: of(coretypes.KnowledgeName),
				Size:  of(10000),
			},
		},
	}

	res, err := search.NewSearchFunc(s.client)().
		Index(indexName).
		Request(query).
		Do(ctx)
	if err != nil {
		return nil, err
	}

	if res.Aggregations == nil {
		g.Log().Infof(ctx, "No aggregations found")
		return nil, nil
	}

	termsAgg, ok := res.Aggregations[names].(*types.StringTermsAggregate)
	if !ok || termsAgg == nil {
		return nil, errors.New("failed to parse terms aggregation")
	}

	var list []string
	for _, bucket := range termsAgg.Buckets.([]types.StringTermsBucket) {
		list = append(list, bucket.Key.(string))
	}

	return list, nil
}

// SearchDocuments 搜索文档
func (s *ESVectorStore) SearchDocuments(ctx context.Context, req *SearchRequest) (*SearchResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sreq := search.NewRequest()
	sreq.Query = req.Query.(*types.Query)
	sreq.Size = of(req.Size)

	resp, err := search.NewSearchFunc(s.client)().
		Index(req.IndexName).
		Request(sreq).
		Do(ctx)
	if err != nil {
		return nil, err
	}

	var docs []*schema.Document
	for _, hit := range resp.Hits.Hits {
		doc, err := s.esHit2Document(ctx, hit)
		if err != nil {
			g.Log().Errorf(ctx, "esHit2Document failed, err=%v", err)
			continue
		}
		docs = append(docs, doc)
	}

	return &SearchResponse{
		Documents: docs,
		Total:     resp.Hits.Total.Value,
	}, nil
}

// esHit2Document 将 ES Hit 转换为 Document
func (s *ESVectorStore) esHit2Document(ctx context.Context, hit types.Hit) (*schema.Document, error) {
	doc := &schema.Document{
		ID:       *hit.Id_,
		MetaData: map[string]any{},
	}

	var src map[string]any
	if err := sonic.Unmarshal(hit.Source_, &src); err != nil {
		return nil, err
	}

	for field, val := range src {
		switch field {
		case coretypes.FieldContent:
			doc.Content = val.(string)
		case coretypes.FieldContentVector:
			var v []float64
			for _, item := range val.([]interface{}) {
				v = append(v, item.(float64))
			}
			doc.WithDenseVector(v)
		case coretypes.FieldQAContentVector, coretypes.FieldQAContent:
			// 这两个字段都不返回
		case coretypes.FieldExtra:
			if val == nil {
				continue
			}
			doc.MetaData[coretypes.FieldExtra] = val.(string)
		case coretypes.KnowledgeName:
			doc.MetaData[coretypes.KnowledgeName] = val.(string)
		default:
			return nil, fmt.Errorf("unexpected field=%s, val=%v", field, val)
		}
	}

	if hit.Score_ != nil {
		doc.WithScore(float64(*hit.Score_))
	}

	return doc, nil
}

// Close 关闭连接
func (s *ESVectorStore) Close() error {
	// ES client 不需要显式关闭
	return nil
}

// WriteFingerprint writes the fingerprint to ES _meta field.
// F3: Conditional write — refuses to overwrite if _meta already contains go-rag-fingerprint.
// R6: Full context propagation to all ES API calls.
// C2: Reject empty resourceID.
func (s *ESVectorStore) WriteFingerprint(ctx context.Context, indexName string, fp *Fingerprint, resourceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fp == nil {
		return fmt.Errorf("nil fingerprint")
	}
	// C2: Reject empty resourceID — identity incomplete
	if resourceID == "" {
		return fmt.Errorf("index %q: %w (resourceID empty, refusing to write incomplete identity)",
			indexName, ErrFingerprintCorrupted)
	}

	// F3: Check if fingerprint already exists in _meta
	existingFP, _, readErr := s.ReadFingerprint(ctx, indexName)
	if readErr == nil && existingFP != nil {
		// Fingerprint already exists — refuse to overwrite
		return fmt.Errorf("index %q: %w", indexName, ErrFingerprintExists)
	}
	if readErr != nil && !errors.Is(readErr, ErrFingerprintMissing) {
		// Unexpected read error (not "missing")
		return fmt.Errorf("check existing fingerprint: %w", readErr)
	}

	// Serialize fingerprint to JSON
	fpJSON, err := fp.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal fingerprint: %w", err)
	}
	// Parse back to map for _meta structure
	var fpMap map[string]any
	if err := sonic.Unmarshal(fpJSON, &fpMap); err != nil {
		return fmt.Errorf("unmarshal fingerprint: %w", err)
	}
	// Build _meta with fingerprint and resourceID
	meta := map[string]any{
		"go-rag-fingerprint": fpMap,
		"go-rag-resource-id": resourceID,
	}
	metaJSON, err := sonic.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	// Use put mapping API to update _meta with context (R6)
	body := fmt.Sprintf(`{"_meta": %s}`, string(metaJSON))
	res, err := s.client.Indices.PutMapping(
		[]string{indexName},
		strings.NewReader(body),
		s.client.Indices.PutMapping.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("put mapping: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("put mapping failed: %s", res.String())
	}
	return nil
}

// ReadFingerprint reads the fingerprint from ES _meta field.
// F4: Returns both fingerprint and resourceID.
// R2: Verifies _meta.go-rag-resource-id matches settings.index.uuid.
// R6: Full context propagation to all ES API calls.
// C4: Schema validation — verifies actual vector dimensions match fingerprint.
func (s *ESVectorStore) ReadFingerprint(ctx context.Context, indexName string) (*Fingerprint, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	// Use get index API to read mapping with _meta (R6: pass ctx)
	res, err := s.client.Indices.Get(
		[]string{indexName},
		s.client.Indices.Get.WithContext(ctx),
	)
	if err != nil {
		return nil, "", fmt.Errorf("get index: %w", err)
	}
	defer res.Body.Close()

	// Read body
	var bodyBytes []byte
	if bodyBytes, err = io.ReadAll(res.Body); err != nil {
		return nil, "", fmt.Errorf("read response body: %w", err)
	}

	// Check HTTP status from response
	if res.StatusCode == 404 {
		return nil, "", ErrFingerprintMissing
	}
	if res.IsError() {
		return nil, "", fmt.Errorf("get index failed: %s", res.String())
	}

	// Parse response — include _meta, settings.index.uuid, and mapping properties for schema validation (C4)
	var result map[string]struct {
		Mappings struct {
			Meta       map[string]any            `json:"_meta"`
			Properties map[string]map[string]any `json:"properties"`
		} `json:"mappings"`
		Settings struct {
			Index struct {
				UUID string `json:"uuid"`
			} `json:"index"`
		} `json:"settings"`
	}
	if err := sonic.Unmarshal(bodyBytes, &result); err != nil {
		return nil, "", fmt.Errorf("%w: decode response: %v", ErrFingerprintCorrupted, err)
	}
	// Extract _meta of the index
	indexState, ok := result[indexName]
	if !ok {
		return nil, "", ErrFingerprintMissing
	}
	if indexState.Mappings.Meta == nil {
		return nil, "", ErrFingerprintMissing
	}
	// Extract "go-rag-fingerprint" from _meta
	fpData, ok := indexState.Mappings.Meta["go-rag-fingerprint"]
	if !ok || fpData == nil {
		return nil, "", ErrFingerprintMissing
	}
	// Serialize back to JSON for parsing
	fpJSON, err := sonic.Marshal(fpData)
	if err != nil {
		return nil, "", fmt.Errorf("%w: marshal fingerprint: %v", ErrFingerprintCorrupted, err)
	}
	fp, err := ParseFingerprint(fpJSON)
	if err != nil {
		return nil, "", err
	}
	// Extract resourceID from _meta
	var resourceID string
	if rid, ok := indexState.Mappings.Meta["go-rag-resource-id"].(string); ok {
		resourceID = rid
	}
	// R2: Verify resourceID matches actual ES index UUID
	actualUUID := indexState.Settings.Index.UUID
	if resourceID != "" && actualUUID != "" && resourceID != actualUUID {
		return nil, "", fmt.Errorf("%w: resource-id mismatch (meta=%s, actual=%s)",
			ErrFingerprintCorrupted, resourceID, actualUUID)
	}
	// C4: Schema validation — verify actual vector dimensions match fingerprint
	for _, field := range []string{coretypes.FieldContentVector, coretypes.FieldQAContentVector} {
		prop, fieldOK := indexState.Mappings.Properties[field]
		if !fieldOK {
			continue // field missing from mapping — skip (schema might be old)
		}
		dimsVal, dimsOK := prop["dims"]
		if !dimsOK {
			continue
		}
		actualDims, ok := dimsVal.(float64)
		if !ok {
			continue
		}
		if int(actualDims) != fp.Dimensions {
			return nil, "", fmt.Errorf("%w: field %q expected dims=%d, got dims=%d",
				ErrFingerprintMismatch, field, fp.Dimensions, int(actualDims))
		}
	}
	return fp, resourceID, nil
}

// ReadESIndexUUID reads the ES-assigned UUID for the given index.
// F1: Used to bind physical resource identity after creation.
// R6: Full context propagation.
func (s *ESVectorStore) ReadESIndexUUID(ctx context.Context, indexName string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	res, err := s.client.Indices.Get(
		[]string{indexName},
		s.client.Indices.Get.WithContext(ctx),
	)
	if err != nil {
		return "", fmt.Errorf("get index for UUID: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return "", fmt.Errorf("get index for UUID failed: %s", res.String())
	}
	var bodyBytes []byte
	if bodyBytes, err = io.ReadAll(res.Body); err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}
	var result map[string]struct {
		Settings struct {
			Index struct {
				UUID string `json:"uuid"`
			} `json:"index"`
		} `json:"settings"`
	}
	if err := sonic.Unmarshal(bodyBytes, &result); err != nil {
		return "", fmt.Errorf("unmarshal index settings: %w", err)
	}
	state, ok := result[indexName]
	if !ok {
		return "", fmt.Errorf("index %q not found in response", indexName)
	}
	return state.Settings.Index.UUID, nil
}

// withRetry 包装函数，添加重试机制
func (s *ESVectorStore) withRetry(operation func() error) error {
	b := backoff.NewExponentialBackOff()
	b.MaxElapsedTime = 30 * time.Second
	return backoff.Retry(operation, b)
}

// of 辅助函数，返回指针
func of[T any](v T) *T {
	return &v
}
