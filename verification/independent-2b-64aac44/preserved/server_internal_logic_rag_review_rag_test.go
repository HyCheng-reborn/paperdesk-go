package rag

import (
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "sync/atomic"
 "testing"

 "github.com/gogf/gf/v2/container/gvar"
 "github.com/wangle201210/go-rag/server/core/common"
 "github.com/wangle201210/go-rag/server/core/config"
 "github.com/wangle201210/go-rag/server/core/providers"
 "github.com/wangle201210/go-rag/server/core/vector"
)

func TestReviewEmbeddingProductionGate(t *testing.T){
 var sends atomic.Int64
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){sends.Add(1);w.Header().Set("Content-Type","application/json");_=json.NewEncoder(w).Encode(map[string]any{"data":[]map[string]any{{"index":0,"embedding":make([]float64,128)}},"usage":map[string]int{"prompt_tokens":1,"total_tokens":1}})}));defer srv.Close()
 pc:=&config.ProviderConfig{}
 err:=gvar.New(map[string]any{"name":"bailian","base_url":srv.URL,"api_key":"offline-key","chat_model":"qwen-plus","embedding_model":"text-embedding-v4","embedding_dimensions":128}).Scan(pc)
 if err!=nil {t.Fatal(err)}
 if pc.BaseURL!=srv.URL||pc.EmbeddingModel!="text-embedding-v4"||pc.EmbeddingDimensions!=128 {t.Fatalf("production ProviderConfig scan lost fields: model=%q dimensions=%d",pc.EmbeddingModel,pc.EmbeddingDimensions)}
 reg,err:=buildRegistry(context.Background(),pc)
 if err!=nil {return} // a production enable gate may reject before model construction
 old:=common.GetRegistry();defer common.SetRegistry(old);common.SetRegistry(reg)
 emb,err:=common.NewEmbedding(context.Background(),&config.Config{Provider:pc,EmbeddingModel:"existing-embedding-model",IndexName:"existing-1024-index"})
 if err!=nil {return}
 got,err:=emb.EmbedStrings(context.Background(),[]string{"document"})
 if err==nil&&sends.Load()!=0 {t.Fatalf("new embedding profile enabled via production config without identity/dimension guard: sends=%d returnedDimensions=%d",sends.Load(),len(got[0]))}
}

func TestReviewRegistryPublicationOnInitFailure(t *testing.T){
 old:=common.GetRegistry();defer common.SetRegistry(old)
 previous:=providers.NewFakeRegistry();common.SetRegistry(previous)
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Header().Set("X-Elastic-Product","Elasticsearch");w.WriteHeader(http.StatusUnauthorized)}));defer srv.Close()
 vs,err:=vector.NewESVectorStore(&vector.ESConfig{Address:srv.URL});if err!=nil {t.Fatal(err)};defer vs.Close()
 cfg:=validRagConfig();cfg.VectorCfg.Type="es";cfg.Provider=&config.ProviderConfig{Name:"bailian",APIKey:"offline-key",BaseURL:"http://example.invalid/v1",ChatModel:"new-model"}
 _,err=(&defaultRagFactory{}).Create(cfg,vs)
 if err==nil {t.Fatal("fixture must fail on local mocked index HEAD")}
 if common.GetRegistry()!=previous {t.Fatal("failed core.New published and retained replacement Registry")}
}

func TestReviewUnknownProviderRejected(t *testing.T){
 reg,err:=buildRegistry(context.Background(),&config.ProviderConfig{Name:"review-unknown-provider",APIKey:"offline-key",BaseURL:"http://example.invalid/v1",ChatModel:"model"})
 if err!=nil {return}
 _,err=reg.GetChat(context.Background())
 if err==nil {t.Fatal("unknown Provider silently constructed generic OpenAI model")}
}
