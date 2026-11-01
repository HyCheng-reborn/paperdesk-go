// Package testutil provides offline fixtures; no real infrastructure is used.
package testutil

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func Qdrant(t *testing.T, observe func(any)) *qdrant.Client {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
		if observe != nil {
			observe(req)
		}
		switch req.(type) {
		case *qdrant.CollectionExistsRequest:
			return &qdrant.CollectionExistsResponse{Result: &qdrant.CollectionExists{Exists: true}}, nil
		case *qdrant.CreateCollection:
			return &qdrant.CollectionOperationResponse{Result: true}, nil
		case *qdrant.QueryPoints:
			return &qdrant.QueryResponse{}, nil
		default:
			return &qdrant.PointsOperationResponse{Result: &qdrant.UpdateResult{Status: qdrant.UpdateStatus_Completed}}, nil
		}
	}))
	qdrant.RegisterCollectionsServer(s, &qdrant.UnimplementedCollectionsServer{})
	qdrant.RegisterPointsServer(s, &qdrant.UnimplementedPointsServer{})
	go s.Serve(lis)
	c, e := qdrant.NewClient(&qdrant.Config{Host: "127.0.0.1", SkipCompatibilityCheck: true, GrpcOptions: []grpc.DialOption{grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() })}})
	if e != nil {
		s.Stop()
		lis.Close()
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close(); s.Stop(); lis.Close() })
	return c
}

// EmbeddingHotIndex 是合成 one-hot 向量的非零分量下标。
// 全零向量在 cosine 脚本下退化，且与 A-4 L0 样本使用的非零合成向量不一致。
// 取 0 是为了与 L0 的具体合成向量对齐：e0 = [1,0,…,0]（1024 维）。
const EmbeddingHotIndex = 0

// OneHotVector 返回 dim 维 one-hot 合成向量：仅 hot 处为 1.0。
func OneHotVector(dim, hot int) []float64 {
	v := make([]float64, dim)
	if hot >= 0 && hot < dim {
		v[hot] = 1
	}
	return v
}

func Embedding(t *testing.T, dim int) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Error(e)
		}
		if req.Dimensions != dim {
			t.Errorf("sent dimensions=%d want=%d", req.Dimensions, dim)
		}
		data := []any{}
		for i := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": OneHotVector(dim, EmbeddingHotIndex)})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(s.Close)
	return s
}
