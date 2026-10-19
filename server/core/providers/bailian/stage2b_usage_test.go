package bailian

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// ---------------------------------------------------------------------------
// B7: Embedding usage per-call 隔离（不再经角色级共享字段串账）
// ---------------------------------------------------------------------------

// embedResp 构造一个带 usage 的单向量 Embedding 响应（dim=3, index=0）。
func embedResp(promptTokens int) map[string]any {
	return map[string]any{
		"object": "list",
		"model":  "test-embed",
		"data": []map[string]any{
			{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}},
		},
		"usage": map[string]any{
			"prompt_tokens": promptTokens,
			"total_tokens":  promptTokens,
		},
	}
}

// TestEmbedding_ConcurrentUsageIsolated 用确定交错的双请求验证 usage 归属正确：
// 单个角色实例并发处理 "AAA"(usage=111) 与 "BBB"(usage=222)，
// 两个 RequestBudget 分别只结算各自的 usage，绝不串账。
func TestEmbedding_ConcurrentUsageIsolated(t *testing.T) {
	// 屏障：强制两个 HTTP 请求同时在途（确定交错），再各自响应。
	var mu sync.Mutex
	arrived := 0
	bothHere := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var probe struct {
			Input []string `json:"input"`
		}
		_ = json.Unmarshal(raw, &probe)

		// 到达计数 → 两个请求都到达后放行（确定交错）。
		mu.Lock()
		arrived++
		if arrived == 2 {
			close(bothHere)
		}
		mu.Unlock()
		select {
		case <-bothHere:
		case <-time.After(5 * time.Second):
		}

		prompt := 0
		if len(probe.Input) > 0 {
			switch probe.Input[0] {
			case "AAA":
				prompt = 111
			case "BBB":
				prompt = 222
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(embedResp(prompt))
	}))
	defer srv.Close()

	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	transport := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	profile := mustProfile(t, providers.ProviderProfile{
		ProviderName: ProviderBailian,
		Protocol:     "openai-compatible",
		Endpoint:     srv.URL,
		Model:        "test-embed",
		Dimensions:   3,
	})

	// 单个角色实例（缓存角色）——缺陷下 lastUsage 会跨请求串账。
	role, err := BuildEmbed(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: transport,
		Budget:    budget,
	})
	if err != nil {
		t.Fatalf("BuildEmbed: %v", err)
	}

	// 两个独立 RequestBudget（同一 ProcessBudget），分别注入各自 ctx。
	rbA, _ := budget.NewRequestBudget(context.Background())
	rbB, _ := budget.NewRequestBudget(context.Background())
	ctxA := providers.ContextWithRequestBudget(context.Background(), rbA)
	ctxB := providers.ContextWithRequestBudget(context.Background(), rbB)

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, e := role.EmbedStrings(ctxA, []string{"AAA"}); e != nil {
			errCh <- e
		}
	}()
	go func() {
		defer wg.Done()
		if _, e := role.EmbedStrings(ctxB, []string{"BBB"}); e != nil {
			errCh <- e
		}
	}()

	doneWait := make(chan struct{})
	go func() { wg.Wait(); close(doneWait) }()
	select {
	case <-doneWait:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for concurrent EmbedStrings")
	}
	close(errCh)
	for e := range errCh {
		t.Fatalf("EmbedStrings error: %v", e)
	}

	// 归属核对：rbA 只结算 111，rbB 只结算 222（无串账）。
	if got := rbA.Snapshot().InputUsed; got != 111 {
		t.Errorf("rbA InputUsed = %d, want 111 (usage cross-talk?)", got)
	}
	if got := rbB.Snapshot().InputUsed; got != 222 {
		t.Errorf("rbB InputUsed = %d, want 222 (usage cross-talk?)", got)
	}
}
