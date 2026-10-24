package vector

import (
	"context"
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

// CreateIndex 创建索引
func (s *ESVectorStore) CreateIndex(ctx context.Context, indexName string) error {
	mapping, err := config.ESMapping(s.dimensions)
	if err != nil {
		return err
	}
	_, err = create.NewCreateFunc(s.client)(indexName).Request(&create.Request{Mappings: mapping}).Do(ctx)
	return err
}

// IndexExists 检查索引是否存在
func (s *ESVectorStore) IndexExists(ctx context.Context, indexName string) (bool, error) {
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
func (s *ESVectorStore) WriteFingerprint(ctx context.Context, indexName string, fp *Fingerprint) error {
	if fp == nil {
		return fmt.Errorf("nil fingerprint")
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
	// Build _meta with fingerprint under "go-rag-fingerprint" key
	meta := map[string]any{
		"go-rag-fingerprint": fpMap,
	}
	metaJSON, err := sonic.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	// Use put mapping API to update _meta
	body := fmt.Sprintf(`{"_meta": %s}`, string(metaJSON))
	res, err := s.client.Indices.PutMapping(
		[]string{indexName},
		strings.NewReader(body),
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
func (s *ESVectorStore) ReadFingerprint(ctx context.Context, indexName string) (*Fingerprint, error) {
	// Use get index API to read mapping with _meta
	res, err := s.client.Indices.Get([]string{indexName})
	if err != nil {
		return nil, fmt.Errorf("get index: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		if res.StatusCode == 404 {
			return nil, ErrFingerprintMissing
		}
		return nil, fmt.Errorf("get index failed: %s", res.String())
	}
	// Read body
	var bodyBytes []byte
	if bodyBytes, err = io.ReadAll(res.Body); err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	// Parse response
	var result map[string]struct {
		Mappings struct {
			Meta map[string]any `json:"_meta"`
		} `json:"mappings"`
	}
	if err := sonic.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("%w: decode response: %v", ErrFingerprintCorrupted, err)
	}
	// Extract _meta for the index
	indexState, ok := result[indexName]
	if !ok {
		return nil, ErrFingerprintMissing
	}
	if indexState.Mappings.Meta == nil {
		return nil, ErrFingerprintMissing
	}
	// Extract "go-rag-fingerprint" from _meta
	fpData, ok := indexState.Mappings.Meta["go-rag-fingerprint"]
	if !ok || fpData == nil {
		return nil, ErrFingerprintMissing
	}
	// Serialize back to JSON for parsing
	fpJSON, err := sonic.Marshal(fpData)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal fingerprint: %v", ErrFingerprintCorrupted, err)
	}
	return ParseFingerprint(fpJSON)
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
