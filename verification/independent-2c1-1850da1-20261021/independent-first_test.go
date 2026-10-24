package rag
import (
 "testing"
 "errors"
 "fmt"
 "github.com/gogf/gf/v2/frame/g"
 "github.com/gogf/gf/v2/os/gcfg"
 "github.com/wangle201210/go-rag/server/core/config"
)
func TestIndependentYAMLProductionReader(t *testing.T) {
 old:=g.Cfg().GetAdapter(); defer g.Cfg().SetAdapter(old)
 for _,tc:=range []struct{raw string; invalid bool}{ {"768",false},{"0",true},{"-2",true},{"\"768\"",true},{"1.5",true},{"true",true} } {
  t.Run(tc.raw,func(t *testing.T){
   a,e:=gcfg.NewAdapterContent(fmt.Sprintf("vector:\n  type: es\n  indexName: offline\nembedding:\n  dimensions: %s\n",tc.raw)); if e!=nil {t.Fatal(e)}; g.Cfg().SetAdapter(a)
   c,e:=readRagConfig(); if tc.invalid { if !errors.Is(e,config.ErrEmbeddingDimensions){t.Fatalf("want identifiable rejection got %v",e)};return }; if e!=nil {t.Fatal(e)}; n,e:=normalizeDimensions(c);if e!=nil || n.EmbeddingDimensions!=768 || n.VectorCfg.EmbeddingDimensions!=768 {t.Fatalf("%+v %v",n,e)}
  })
 }
}
