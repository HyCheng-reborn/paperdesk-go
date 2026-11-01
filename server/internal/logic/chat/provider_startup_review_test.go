package chat

import (
	"errors"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gcfg"
	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/providers"
	"os"
	"path/filepath"
	"testing"
)

type startupBudgetRegistry struct {
	*providers.FakeRegistry
	owner *providers.ProcessBudget
}

func (r *startupBudgetRegistry) Budget() *providers.ProcessBudget { return r.owner }

func startupYAML(t *testing.T, content string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := gcfg.NewAdapterFile(p)
	if err != nil {
		t.Fatal(err)
	}
	old := g.Cfg().GetAdapter()
	g.Cfg().SetAdapter(a)
	t.Cleanup(func() { g.Cfg().SetAdapter(old) })
}

// Reconstructed from the independent review, not its original overlay.
func TestReviewProviderRoleStartup(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		chat       bool
	}{
		{"provider_chat_no_legacy", "provider:\n  chat_model: provider-chat\n", true},
		{"provider_embedding_legacy_chat", "provider:\n  embedding_model: embed\nchat:\n  apiKey: offline\n  baseURL: http://127.0.0.1:1/v1\n  model: legacy-chat\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetChatState()
			resetDAOState()
			t.Cleanup(resetChatState)
			done := initDAOForTest(t)
			t.Cleanup(done)
			startupYAML(t, tc.yaml)
			reg := providers.NewFakeRegistry()
			if !tc.chat {
				reg.Chat = nil
			}
			pb, err := providers.NewProcessBudget(5, 100, 100, 1)
			if err != nil {
				t.Fatal(err)
			}
			common.SetRegistry(&startupBudgetRegistry{FakeRegistry: reg, owner: pb})
			t.Cleanup(func() { common.SetRegistry(nil) })
			if err := InitWith(nil, nil); err != nil {
				t.Fatalf("startup: %v", err)
			}
			if !tc.chat {
				if _, ok := GetChat().cm.(providers.StreamSessioner); !ok {
					t.Fatal("legacy chat missing budget session wrapper")
				}
			}
		})
	}
}

func TestReviewConfiguredProviderFailureDoesNotFallback(t *testing.T) {
	resetChatState()
	resetDAOState()
	t.Cleanup(resetChatState)
	done := initDAOForTest(t)
	t.Cleanup(done)
	startupYAML(t, "provider:\n  chat_model: provider-chat\nchat:\n  apiKey: offline\n  baseURL: http://127.0.0.1:1/v1\n  model: legacy-chat\n")
	reg := providers.NewFakeRegistry()
	reg.Chat = nil
	common.SetRegistry(reg)
	t.Cleanup(func() { common.SetRegistry(nil) })
	if err := InitWith(nil, nil); !errors.Is(err, providers.ErrRoleNotConfigured) {
		t.Fatalf("expected provider error, got %v", err)
	}
	if IsInitialized() {
		t.Fatal("failed provider must not initialize chat")
	}
}

func TestReviewLegacyFallbackWithoutBudgetFailsClosed(t *testing.T) {
	resetChatState()
	resetDAOState()
	t.Cleanup(resetChatState)
	done := initDAOForTest(t)
	t.Cleanup(done)
	startupYAML(t, "provider:\n  embedding_model: embed\nchat:\n  apiKey: offline\n  baseURL: http://127.0.0.1:1/v1\n  model: legacy-chat\n")
	common.SetRegistry(providers.NewFakeRegistry())
	t.Cleanup(func() { common.SetRegistry(nil) })
	if err := InitWith(nil, nil); !errors.Is(err, common.ErrLegacyRoleBudgetUnavailable) {
		t.Fatalf("expected missing budget error, got %v", err)
	}
	if IsInitialized() {
		t.Fatal("legacy fallback without budget must not initialize chat")
	}
}
