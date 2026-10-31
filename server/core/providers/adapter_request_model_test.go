package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// TestRequestModelIDRejectsVersionedIdentity locks the single rule used by every
// role builder: the wire model must equal the verified identity, so an identity
// carrying "@version" (which neither supported protocol can express) fails closed.
func TestRequestModelIDRejectsVersionedIdentity(t *testing.T) {
	cases := []struct {
		modelID string
		want    string
		wantErr error
	}{
		{"text-embedding-v4", "text-embedding-v4", nil},
		{"  text-embedding-v4  ", "text-embedding-v4", nil},
		{"text-embedding-v4@v1", "", ErrModelVersionNotExpressible},
		{"text-embedding-v4@", "", ErrModelIDInvalid},
		{"@v1", "", ErrModelIDInvalid},
		{"m@v1@v2", "", ErrModelIDInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.modelID, func(t *testing.T) {
			got, err := RequestModelID(tc.modelID)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("RequestModelID(%q) err = %v, want %v", tc.modelID, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RequestModelID(%q) unexpected error: %v", tc.modelID, err)
			}
			if got != tc.want {
				t.Fatalf("RequestModelID(%q) = %q, want %q", tc.modelID, got, tc.want)
			}
		})
	}
}

// TestP1_VersionedProfileFailsClosedForAllBuilders covers the shared Profile
// split: a versioned Model must not reach any HTTP request, for chat, embed and
// rerank alike.
func TestP1_VersionedProfileFailsClosedForAllBuilders(t *testing.T) {
	ctx := context.Background()
	var requests atomic.Int64
	tr := NewTransport(WithHTTPClient(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("no request may be issued for a versioned identity")
	})}))

	profile, err := NewProviderProfile(ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     ProtocolOpenAICompat,
		Endpoint:     "https://offline.invalid/v1",
		Model:        "m@v1",
		Dimensions:   3,
	})
	if err != nil {
		t.Fatalf("NewProviderProfile: %v", err)
	}
	// 身份表示保持不变，但请求模型无法表达该身份。
	if got := profile.ModelID(); got != "m@v1" {
		t.Fatalf("ModelID() = %q, want m@v1", got)
	}
	if _, err := profile.RequestModel(); !errors.Is(err, ErrModelVersionNotExpressible) {
		t.Fatalf("RequestModel err = %v, want ErrModelVersionNotExpressible", err)
	}

	builders := []struct {
		name  string
		build func() error
	}{
		{"chat", func() error {
			_, err := BuildChatRole(ctx, BuildConfig{Profile: profile, APIKey: "offline", Transport: tr})
			return err
		}},
		{"embed", func() error {
			_, err := BuildEmbedRole(ctx, BuildConfig{Profile: profile, APIKey: "offline", Transport: tr})
			return err
		}},
		{"rerank", func() error {
			_, err := BuildRerankRole(ctx, BuildConfig{Profile: profile, APIKey: "offline", Transport: tr})
			return err
		}},
	}
	for _, b := range builders {
		t.Run(b.name, func(t *testing.T) {
			err := b.build()
			if !errors.Is(err, ErrModelVersionNotExpressible) {
				t.Fatalf("%s builder err = %v, want ErrModelVersionNotExpressible", b.name, err)
			}
		})
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("fail-closed builders must not issue HTTP requests, got %d", n)
	}
}

// TestP1_WireModelEqualsBoundIdentity inspects the real request bodies: for a
// versionless identity the wire model must be exactly the verified identity.
func TestP1_WireModelEqualsBoundIdentity(t *testing.T) {
	var chatModel, embedModel atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			embedModel.Store(body.Model)
			_, _ = w.Write([]byte(embeddingBody))
			return
		}
		chatModel.Store(body.Model)
		_, _ = w.Write([]byte(chatCompletionBody))
	}))
	defer srv.Close()

	profile, err := NewProviderProfile(ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     ProtocolOpenAICompat,
		Endpoint:     srv.URL + "/v1",
		Model:        "text-embedding-v4",
		Dimensions:   3,
	})
	if err != nil {
		t.Fatalf("NewProviderProfile: %v", err)
	}

	ctx := context.Background()
	tr := NewTransport()

	embedRole, err := BuildEmbedRole(ctx, BuildConfig{Profile: profile, APIKey: "offline", Transport: tr})
	if err != nil {
		t.Fatalf("BuildEmbedRole: %v", err)
	}
	if _, err := embedRole.EmbedStrings(ctx, []string{"a", "b"}); err != nil {
		t.Fatalf("EmbedStrings: %v", err)
	}

	chatRole, err := BuildChatRole(ctx, BuildConfig{Profile: profile, APIKey: "offline", Transport: tr})
	if err != nil {
		t.Fatalf("BuildChatRole: %v", err)
	}
	if _, err := chatRole.Generate(ctx, []*schema.Message{schema.UserMessage("hi")}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if got := embedRole.ModelID(); got != "text-embedding-v4" {
		t.Fatalf("embed identity = %q", got)
	}
	if got, _ := embedModel.Load().(string); got != embedRole.ModelID() {
		t.Fatalf("embed HTTP model = %q, identity = %q", got, embedRole.ModelID())
	}
	chatProfile := chatRole.(interface{ Profile() ProviderProfile }).Profile()
	chatIdentity := chatProfile.ModelID()
	if got, _ := chatModel.Load().(string); got != chatIdentity {
		t.Fatalf("chat HTTP model = %q, identity = %q", got, chatIdentity)
	}
}
