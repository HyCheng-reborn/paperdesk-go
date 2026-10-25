package vector

import (
	"bytes"
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
// D2: 创建成功后只读获取 UUID 用于日志；不再 PutMapping 后补；不再 deleteIndexBestEffort。
// 若索引已存在返回 ErrIndexAlreadyExists；任何创建后验证失败返回错误（不删除索引）。
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

	// D2: 创建成功后只读获取 UUID（仅用于日志，不写回 _meta）
	actualUUID, uuidErr := s.ReadESIndexUUID(ctx, indexName)
	if uuidErr != nil {
		// D2: 不删除索引，保留资源供人工诊断
		return fmt.Errorf("read back index UUID after creation: %w", uuidErr)
	}
	if actualUUID == "" {
		return fmt.Errorf("ES returned empty index UUID for %q", indexName)
	}
	g.Log().Infof(ctx, "ES index %q created with UUID=%s (read-only, not written to _meta)", indexName, actualUUID)

	// D3: 严格 schema 验证（回读 mapping 确认实际维度/distance/type）
	if verifyErr := s.verifyESSchemaStrict(ctx, indexName, fp); verifyErr != nil {
		// D2: 不删除索引，保留资源供人工诊断
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

// verifyESSchemaStrict 回读 ES mapping 并使用 ValidateSchemaStrict 进行严格校验（D3）。
func (s *ESVectorStore) verifyESSchemaStrict(ctx context.Context, indexName string, fp *Fingerprint) error {
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
	// D3: 使用 json.Decoder.UseNumber 解析，保留 JSON 数字为 json.Number（而非 float64）
	// 这样 strictDims 可以正确区分整数和浮点数
	dec := json.NewDecoder(bytes.NewReader(bodyBytes))
	dec.UseNumber()
	var result map[string]struct {
		Mappings struct {
			Properties map[string]map[string]any `json:"properties"`
		} `json:"mappings"`
	}
	if err := dec.Decode(&result); err != nil {
		return fmt.Errorf("unmarshal schema verification response: %w", err)
	}
	state, ok := result[indexName]
	if !ok {
		return fmt.Errorf("index %q not found in schema verification response", indexName)
	}
	// 构建 FieldInfo map 供 ValidateSchemaStrict 使用
	fields := make(map[string]FieldInfo)
	for _, fieldName := range []string{coretypes.FieldContentVector, coretypes.FieldQAContentVector} {
		prop, fieldOK := state.Mappings.Properties[fieldName]
		if !fieldOK {
			continue // 缺失字段由 ValidateSchemaStrict 检测
		}
		fi := FieldInfo{}
		if ft, ok := prop["type"].(string); ok {
			fi.FieldType = ft
		}
		// ES 返回 dims 为整数（JSON number → float64 via interface{}）
		if dimsVal, ok := prop["dims"]; ok {
			fi.Dims = dimsVal
		}
		// ES 的 similarity 在 index_options 或顶层
		if simVal, ok := prop["similarity"].(string); ok {
			fi.Distance = simVal
		}
		fields[fieldName] = fi
	}
	return ValidateSchemaStrict(fp, fields, "es")
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
// D2: resourceID comes from settings.index.uuid (physical ES identity), NOT from _meta.
// D3: Schema validation uses ValidateSchemaStrict (no silent skipping).
// R6: Full context propagation to all ES API calls.
func (s *ESVectorStore) ReadFingerprint(ctx context.Context, indexName string) (*Fingerprint, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	// Use get index API to read mapping with _meta and settings (R6: pass ctx)
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

	// D3: 使用 json.Decoder.UseNumber 解析，保留 JSON 数字为 json.Number（而非 float64）
	// 这样 strictDims 可以正确区分整数和浮点数
	dec := json.NewDecoder(bytes.NewReader(bodyBytes))
	dec.UseNumber()
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
	if err := dec.Decode(&result); err != nil {
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

	// D2: resourceID 来自 settings.index.uuid（ES 物理资源标识），不从 _meta 读取
	resourceID := indexState.Settings.Index.UUID

	// D3: 严格 schema 验证（不再用 continue 跳过缺失字段）
	fields := make(map[string]FieldInfo)
	for _, fieldName := range []string{coretypes.FieldContentVector, coretypes.FieldQAContentVector} {
		prop, fieldOK := indexState.Mappings.Properties[fieldName]
		if !fieldOK {
			continue // 缺失字段由 ValidateSchemaStrict 检测
		}
		fi := FieldInfo{}
		if ft, ok := prop["type"].(string); ok {
			fi.FieldType = ft
		}
		if dimsVal, ok := prop["dims"]; ok {
			fi.Dims = dimsVal
		}
		if simVal, ok := prop["similarity"].(string); ok {
			fi.Distance = simVal
		}
		fields[fieldName] = fi
	}
	if schemaErr := ValidateSchemaStrict(fp, fields, "es"); schemaErr != nil {
		return nil, "", schemaErr
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
