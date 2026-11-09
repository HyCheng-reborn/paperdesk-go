package bailian

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/wangle201210/go-rag/server/core/providers"
)

type rejectRoundTripFunc func(*http.Request) (*http.Response, error)

func (f rejectRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestP1_BailianBuildersFailClosedForVersionedProfile covers the 百炼 construction
// chain: it forwards to the shared builders, so a versioned profile must be
// rejected before any HTTP request instead of sending the bare model name.
func TestP1_BailianBuildersFailClosedForVersionedProfile(t *testing.T) {
	var requests atomic.Int64
	tr := providers.NewTransport(providers.WithHTTPClient(&http.Client{
		Transport: rejectRoundTripFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return nil, errors.New("no request may be issued for a versioned identity")
		}),
	}))

	p, err := providers.NewProviderProfile(providers.ProviderProfile{
		ProviderName: "bailian",
		Protocol:     providers.ProtocolOpenAICompat,
		Endpoint:     "https://offline.invalid/compatible-mode/v1",
		Model:        "text-embedding-v4@2024",
		Dimensions:   3,
	})
	if err != nil {
		t.Fatalf("NewProviderProfile: %v", err)
	}
	if got := p.ModelID(); got != "text-embedding-v4@2024" {
		t.Fatalf("ModelID() = %q", got)
	}

	ctx := context.Background()
	if _, err := BuildEmbed(ctx, providers.BuildConfig{Profile: p, APIKey: "offline", Transport: tr}); !errors.Is(err, providers.ErrModelVersionNotExpressible) {
		t.Fatalf("BuildEmbed err = %v, want ErrModelVersionNotExpressible", err)
	}
	if _, err := BuildChat(ctx, providers.BuildConfig{Profile: p, APIKey: "offline", Transport: tr}); !errors.Is(err, providers.ErrModelVersionNotExpressible) {
		t.Fatalf("BuildChat err = %v, want ErrModelVersionNotExpressible", err)
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("fail-closed bailian builders must not issue HTTP requests, got %d", n)
	}
}
