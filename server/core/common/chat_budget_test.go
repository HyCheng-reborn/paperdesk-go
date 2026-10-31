package common

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/providers"
	_ "github.com/wangle201210/go-rag/server/core/providers/openai"
)

// chatServer counts real chat requests and answers with a minimal completion.
func chatServer(t *testing.T, hits *atomic.Int64, modelID string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 1,
			"model":   modelID,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// embedServer counts real embedding requests; the budget test asserts it stays 0.
func embedServer(t *testing.T, hits *atomic.Int64, modelID string, dims int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		hits.Add(1)
		vec := make([]float64, dims)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{{
				"object": "embedding", "index": 0, "embedding": vec,
			}},
			"model": modelID,
			"usage": map[string]any{"prompt_tokens": 1, "total_tokens": 1},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mustProfile(t *testing.T, in providers.ProviderProfile) *providers.ProviderProfile {
	t.Helper()
	p, err := providers.NewProviderProfile(in)
	if err != nil {
		t.Fatalf("new provider profile: %v", err)
	}
	return p
}

// TestP11_QAAndEmbeddingShareRegistryBudget is the budget-ceiling=1 case: after a
// QA generation consumes the only process attempt, an embedding request from the
// same Registry must be rejected before any model HTTP call.
func TestP11_QAAndEmbeddingShareRegistryBudget(t *testing.T) {
	var chatHits, embedHits atomic.Int64
	chatSrv := chatServer(t, &chatHits, "chat-model")
	embedSrv := embedServer(t, &embedHits, "embed-model", 2)

	budget, err := providers.NewProcessBudget(1, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := providers.NewRegistry(providers.RegistryConfig{
		Chat: mustProfile(t, providers.ProviderProfile{
			ProviderName: "openai",
			Protocol:     providers.ProtocolOpenAICompat,
			Endpoint:     chatSrv.URL,
			Model:        "chat-model",
			Capabilities: providers.Capabilities{Stream: true, Usage: true},
		}),
		Embed: mustProfile(t, providers.ProviderProfile{
			ProviderName: "openai",
			Protocol:     providers.ProtocolOpenAICompat,
			Endpoint:     embedSrv.URL,
			Model:        "embed-model",
			Dimensions:   2,
		}),
		Budget:      budget,
		Credentials: func(context.Context, *providers.ProviderProfile) (string, error) { return "test-key", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	conf := &config.Config{
		EmbeddingDimensions: 2,
		Provider: &config.ProviderConfig{
			Name:                "openai",
			ChatModel:           "chat-model",
			EmbeddingModel:      "embed-model",
			EmbeddingDimensions: 2,
		},
		Registry: reg,
	}
	bindTestTarget(t, conf)

	// QA→Chat fallback: no QA profile is configured, so GetQA must use the Chat
	// profile (and its budget) rather than a legacy model.
	qa, err := GetQAModelForRag(context.Background(), conf)
	if err != nil {
		t.Fatalf("resolve QA model: %v", err)
	}
	if _, err := qa.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err != nil {
		t.Fatalf("QA generate: %v", err)
	}
	if chatHits.Load() != 1 {
		t.Fatalf("expected exactly 1 chat request, got %d", chatHits.Load())
	}

	eb, err := NewEmbedding(context.Background(), conf)
	if err != nil {
		t.Fatalf("new embedding: %v", err)
	}
	if _, err := eb.EmbedStrings(context.Background(), []string{"hello"}); !errors.Is(err, providers.ErrBudgetExhausted) {
		t.Fatalf("expected budget exhaustion, got: %v", err)
	}
	if embedHits.Load() != 0 {
		t.Fatalf("embedding request escaped budget: %d HTTP calls", embedHits.Load())
	}
	snap := budget.Snapshot()
	if snap.CommittedAttempts != 1 || snap.AttemptsUsed != 1 {
		t.Fatalf("budget snapshot = %+v, want exactly one committed attempt", snap)
	}
}

// countingChatRole counts invocations without any HTTP dependency.
type countingChatRole struct {
	calls int
}

func (c *countingChatRole) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	c.calls++
	return schema.AssistantMessage("ok", nil), nil
}

func (c *countingChatRole) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not used in this test")
}

// budgetRegistry exposes a ProcessBudget without building real roles.
type budgetRegistry struct {
	*fakeRegistry
	budget *providers.ProcessBudget
}

func (r *budgetRegistry) Budget() *providers.ProcessBudget { return r.budget }

// TestP11_LegacyFallbackRequiresBudgetOwner proves the legacy fallback cannot
// bypass the Registry budget: without a budget owner it fails closed, with one
// it is wrapped by the production budget adapter.
func TestP11_LegacyFallbackRequiresBudgetOwner(t *testing.T) {
	inner := &countingChatRole{}

	if _, err := wrapLegacyWithRegistryBudget(inner, &fakeRegistry{}, "chat"); !errors.Is(err, ErrLegacyRoleBudgetUnavailable) {
		t.Fatalf("registry without budget owner must fail closed, got: %v", err)
	}

	budget, err := providers.NewProcessBudget(1, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := wrapLegacyWithRegistryBudget(inner, &budgetRegistry{fakeRegistry: &fakeRegistry{}, budget: budget}, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Generate(context.Background(), []*schema.Message{schema.UserMessage("one")}); err != nil {
		t.Fatalf("first legacy generate: %v", err)
	}
	if _, err := wrapped.Generate(context.Background(), []*schema.Message{schema.UserMessage("two")}); !errors.Is(err, providers.ErrBudgetExhausted) {
		t.Fatalf("second legacy generate must be budgeted, got: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner legacy model calls = %d, want 1", inner.calls)
	}
}

// TestP11_LegacyChatUsesRegistryBudget covers the legacy Chat resolution path
// used by core.New: the real HTTP request is budgeted by the Registry owner.
func TestP11_LegacyChatUsesRegistryBudget(t *testing.T) {
	var chatHits atomic.Int64
	chatSrv := chatServer(t, &chatHits, "legacy-chat")

	budget, err := providers.NewProcessBudget(1, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	conf := &config.Config{
		ChatModel:   "legacy-chat",
		ChatAPIKey:  "legacy-key",
		ChatBaseURL: chatSrv.URL,
		Registry:    &budgetRegistry{fakeRegistry: &fakeRegistry{}, budget: budget},
	}
	cm, err := GetChatModelForRag(context.Background(), conf)
	if err != nil {
		t.Fatalf("resolve legacy chat: %v", err)
	}
	if _, err := cm.Generate(context.Background(), []*schema.Message{schema.UserMessage("one")}); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if _, err := cm.Generate(context.Background(), []*schema.Message{schema.UserMessage("two")}); !errors.Is(err, providers.ErrBudgetExhausted) {
		t.Fatalf("second generate must be budgeted, got: %v", err)
	}
	if chatHits.Load() != 1 {
		t.Fatalf("expected exactly 1 chat HTTP request, got %d", chatHits.Load())
	}
}
