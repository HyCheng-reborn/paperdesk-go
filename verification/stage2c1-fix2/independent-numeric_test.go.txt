package rag
import("testing";"errors";"github.com/wangle201210/go-rag/server/core/config")
func TestIndependentYAMLNumericForms(t *testing.T){
 for _,key:=range []string{"legacy","provider"}{for _,raw:=range []string{"768.0","1e3","1.0e3","1.5","\"768\"","0","-1","9223372036854775808"}{t.Run(key+"/"+raw,func(t *testing.T){
  a,b:="","";if key=="legacy" {a=raw}else{b=raw};dimensionsYAMLFile(t,a,b)
  _,e:=readRagConfig();if !errors.Is(e,config.ErrEmbeddingDimensions){t.Fatalf("invalid YAML numeric form accepted: %s; err=%v",raw,e)}
 })}}
}
