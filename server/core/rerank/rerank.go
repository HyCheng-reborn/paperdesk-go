package rerank

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/frame/g"
)

// HTTPDoer is the interface for executing HTTP requests.
// It is satisfied by *http.Client and can be mocked in tests.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// RerankConfig holds the configuration for the rerank service.
type RerankConfig struct {
	URL             string
	APIKey          string
	Model           string
	ReturnDocuments bool
	MaxChunksPerDoc int
	OverlapTokens   int
}

// Conf is kept for backward compatibility with config loading.
type Conf struct {
	Model           string `json:"model"`
	ReturnDocuments bool   `json:"return_documents"`
	MaxChunksPerDoc int    `json:"max_chunks_per_doc"`
	OverlapTokens   int    `json:"overlap_tokens"`
	url             string
	apiKey          string
}

// ToRerankConfig converts Conf to RerankConfig.
func (c *Conf) ToRerankConfig() *RerankConfig {
	return &RerankConfig{
		URL:             c.url,
		APIKey:          c.apiKey,
		Model:           c.Model,
		ReturnDocuments: c.ReturnDocuments,
		MaxChunksPerDoc: c.MaxChunksPerDoc,
		OverlapTokens:   c.OverlapTokens,
	}
}

type Data struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

type Req struct {
	*Data
	*Conf
}

// Result represents a single rerank result item.
// Pointer fields allow detection of missing JSON values.
type Result struct {
	Index          *int     `json:"index"`
	RelevanceScore *float64 `json:"relevance_score"`
}

type Resp struct {
	ID      string    `json:"id"`
	Results []*Result `json:"results"`
}

// Rerank is the rerank service client.
type Rerank struct {
	cfg    *RerankConfig
	client HTTPDoer
}

// NewRerank creates a new Rerank instance.
// If client is nil, http.DefaultClient is used.
func NewRerank(cfg *RerankConfig, client HTTPDoer) *Rerank {
	if client == nil {
		client = http.DefaultClient
	}
	return &Rerank{cfg: cfg, client: client}
}

var (
	rerankCfg     *Conf
	rerankCfgOnce sync.Once
	rerankCfgErr  error
)

// GetConf loads the rerank config from GoFrame configuration.
// Uses sync.Once to eliminate race condition on the cached config.
func GetConf(ctx context.Context) (*Conf, error) {
	rerankCfgOnce.Do(func() {
		baseUrl := g.Cfg().MustGet(ctx, "rerank.baseURL").String()
		apiKey := g.Cfg().MustGet(ctx, "rerank.apiKey").String()
		model := g.Cfg().MustGet(ctx, "rerank.model").String()
		url := fmt.Sprintf("%s/rerank", baseUrl)
		rerankCfg = &Conf{
			apiKey:          apiKey,
			Model:           model,
			ReturnDocuments: false,
			MaxChunksPerDoc: 1024,
			OverlapTokens:   80,
			url:             url,
		}
	})
	return rerankCfg, rerankCfgErr
}

// Rerank performs reranking on the given documents.
// It validates inputs before making any HTTP call.
func (r *Rerank) Rerank(ctx context.Context, query string, docs []*schema.Document, topK int) ([]*schema.Document, error) {
	// Input validation
	if len(docs) == 0 {
		return nil, fmt.Errorf("empty docs")
	}
	for i, doc := range docs {
		if doc == nil {
			return nil, fmt.Errorf("nil document at index %d", i)
		}
	}
	if topK <= 0 {
		return nil, fmt.Errorf("invalid topK: %d", topK)
	}

	// Check ctx before making HTTP call
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	data := &Data{
		Query: query,
		TopN:  topK,
	}
	for _, doc := range docs {
		data.Documents = append(data.Documents, doc.Content)
	}

	results, err := r.rerankDoHttp(ctx, data, docs)
	if err != nil {
		return nil, err
	}

	// Build independent output documents, preserving input unchanged
	return r.buildOutput(docs, results)
}

// buildOutput creates independent output documents with scores,
// ensuring input documents are not modified.
func (r *Rerank) buildOutput(docs []*schema.Document, results []*Result) ([]*schema.Document, error) {
	output := make([]*schema.Document, len(results))
	for i, result := range results {
		src := docs[*result.Index]
		// Deep copy Document including metadata map
		cp := &schema.Document{
			ID:       src.ID,
			Content:  src.Content,
			MetaData: copyMetadata(src.MetaData),
		}
		// Set score on the independent copy
		output[i] = cp.WithScore(*result.RelevanceScore)
	}
	return output, nil
}

// copyMetadata performs a shallow copy of a metadata map.
func copyMetadata(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// rerankDoHttp performs the HTTP request to the rerank API with bounded timeout,
// response size limit, status code check, and thorough result validation.
func (r *Rerank) rerankDoHttp(ctx context.Context, data *Data, docs []*schema.Document) ([]*Result, error) {
	// Bind to caller ctx with bounded timeout; caller's earlier deadline wins
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	reqData := &Req{
		Data: data,
		Conf: &Conf{
			Model:           r.cfg.Model,
			ReturnDocuments: r.cfg.ReturnDocuments,
			MaxChunksPerDoc: r.cfg.MaxChunksPerDoc,
			OverlapTokens:   r.cfg.OverlapTokens,
		},
	}

	marshal, err := sonic.Marshal(reqData)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(timeoutCtx, "POST", r.cfg.URL, bytes.NewReader(marshal))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", r.cfg.APIKey))

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	// Status code check
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rerank API returned status %d", resp.StatusCode)
	}

	// Bounded response body read: precise 10 MiB limit
	const maxRespSize = 10 << 20 // 10 MiB = 10,485,760 bytes
	limited := io.LimitReader(resp.Body, maxRespSize+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxRespSize {
		return nil, fmt.Errorf("response exceeds %d bytes limit", maxRespSize)
	}

	// JSON parse
	var res Resp
	if err := sonic.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// Results existence check
	if res.Results == nil {
		return nil, fmt.Errorf("missing results field")
	}
	if len(res.Results) == 0 {
		return nil, fmt.Errorf("empty results")
	}

	// Result count contract: server must not return more than requested
	if len(res.Results) > data.TopN {
		return nil, fmt.Errorf("server returned %d results, expected at most %d", len(res.Results), data.TopN)
	}

	// Validate each result
	seen := make(map[int]bool)
	for i, result := range res.Results {
		if result == nil {
			return nil, fmt.Errorf("result[%d]: null result item", i)
		}
		if result.Index == nil {
			return nil, fmt.Errorf("result[%d]: missing index", i)
		}
		if *result.Index < 0 || *result.Index >= len(docs) {
			return nil, fmt.Errorf("result[%d]: index %d out of range", i, *result.Index)
		}
		if seen[*result.Index] {
			return nil, fmt.Errorf("result[%d]: duplicate index %d", i, *result.Index)
		}
		seen[*result.Index] = true

		if result.RelevanceScore == nil {
			return nil, fmt.Errorf("result[%d]: missing score", i)
		}
		if math.IsNaN(*result.RelevanceScore) || math.IsInf(*result.RelevanceScore, 0) {
			return nil, fmt.Errorf("result[%d]: invalid score %v", i, *result.RelevanceScore)
		}
	}

	return res.Results, nil
}
