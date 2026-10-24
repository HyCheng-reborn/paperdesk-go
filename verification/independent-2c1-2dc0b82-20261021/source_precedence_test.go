package rag
import("os";"path/filepath";"testing";"errors";"github.com/gogf/gf/v2/frame/g";"github.com/gogf/gf/v2/os/gcfg";"github.com/wangle201210/go-rag/server/core/config")
func TestIndependentSourcePrecedenceAndAliases(t *testing.T){
 base:="vector:\n  type: es\n  indexName: offline\n"
 cases:=[]struct{name,body string;bad bool}{
 {"scalar-int","size: &dim 768\nembedding:\n  dimensions: *dim\n",false},
 {"scalar-float","size: &dim 768.0\nembedding:\n  dimensions: *dim\n",true},
 {"merge-override-int","defaults: &d\n  dimensions: 768.0\nembedding:\n  <<: *d\n  dimensions: 768\n",false},
 {"merge-override-float","defaults: &d\n  dimensions: 768\nembedding:\n  <<: *d\n  dimensions: 768.0\n",true},
 }
 for _,c:=range cases{t.Run(c.name,func(t *testing.T){
  p:=filepath.Join(t.TempDir(),"config.yaml");if e:=os.WriteFile(p,[]byte(base+"embedding:\n  dimensions: 512\n"),0600);e!=nil{t.Fatal(e)}
  a,e:=gcfg.NewAdapterFile(p);if e!=nil{t.Fatal(e)};a.SetContent(base+c.body,p);old:=g.Cfg().GetAdapter();g.Cfg().SetAdapter(a);defer func(){g.Cfg().SetAdapter(old);a.RemoveContent(p)}()
  raw,e:=readRagConfig();if c.bad{if !errors.Is(e,config.ErrEmbeddingDimensions){t.Fatal(e)};return};if e!=nil{t.Fatal(e)};n,e:=normalizeDimensions(raw);if e!=nil||n.EmbeddingDimensions!=768{t.Fatalf("source precedence failed %+v %v",n,e)}
 })}
 t.Run("extension-search",func(t *testing.T){
  root:=t.TempDir();p:=filepath.Join(root,"custom.yaml");if e:=os.WriteFile(p,[]byte(base+"embedding:\n  dimensions: 768\n"),0600);e!=nil{t.Fatal(e)};a,e:=gcfg.NewAdapterFile(filepath.Join(root,"custom"));if e!=nil{t.Fatal(e)};old:=g.Cfg().GetAdapter();g.Cfg().SetAdapter(a);defer g.Cfg().SetAdapter(old);raw,e:=readRagConfig();if e!=nil{t.Fatal(e)};if raw.EmbeddingDimensions!=768{t.Fatal(raw.EmbeddingDimensions)}
 })
}
