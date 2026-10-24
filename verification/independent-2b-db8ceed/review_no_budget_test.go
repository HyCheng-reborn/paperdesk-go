package openai_test
import (
 "context"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "time"
 "github.com/cloudwego/eino/schema"
 "github.com/wangle201210/go-rag/server/core/providers"
 "github.com/wangle201210/go-rag/server/core/providers/bailian"
)
func TestIndependentNoBudgetStreamHasOneConsumer(t *testing.T){
 handlerDone:=make(chan struct{})
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){defer close(handlerDone);w.Header().Set("Content-Type","text/event-stream");for _,part:=range []string{"A","B","C"}{_,_=w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\""+part+"\"}}]}\n\n"))};_,_=w.Write([]byte("data: [DONE]\n\n"));w.(http.Flusher).Flush();<-r.Context().Done()}))
 ctx,cancel:=context.WithTimeout(context.Background(),3*time.Second);defer cancel();defer srv.Close()
 role,err:=bailian.BuildChat(ctx,providers.BuildConfig{Profile:supplementProfile(t,srv.URL,"bailian",0),APIKey:"offline",Transport:providers.NewTransport(providers.WithHTTPClient(srv.Client()))});if err!=nil{t.Fatal(err)}
 sr,err:=role.Stream(ctx,[]*schema.Message{schema.UserMessage("hi")});if err!=nil{t.Fatal(err)};defer sr.Close()
 select{case<-handlerDone:case<-time.After(time.Second):cancel();t.Fatal("fixture did not observe adapter drain/cancel")}
 var got strings.Builder
 for{m,e:=sr.Recv();if e!=nil{break};if m!=nil{got.WriteString(m.Content)}}
 if got.String()!="ABC"{t.Fatalf("adapter's extra consumer drained caller's content: got=%q want=ABC",got.String())}
}
