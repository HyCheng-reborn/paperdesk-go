package chat
import (
 "context"
 "errors"
 "fmt"
 "io"
 "net/http"
 "net/http/httptest"
 "testing"
 "time"
 "github.com/cloudwego/eino/schema"
 "github.com/wangle201210/go-rag/server/core/providers"
)
func reviewManagedChat(t *testing.T,block bool)(*Chat,*fakeMessageSaver,context.Context){
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Header().Set("Content-Type","text/event-stream");fmt.Fprint(w,"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n\n");w.(http.Flusher).Flush();if block{<-r.Context().Done()}else{fmt.Fprint(w,"data: [DONE]\n\n")}}))
 ctx,cancel:=context.WithCancel(context.Background());t.Cleanup(func(){cancel();srv.Close()})
 p,e:=providers.NewProviderProfile(providers.ProviderProfile{ProviderName:"openai",Protocol:providers.ProtocolOpenAICompat,Endpoint:srv.URL,Model:"offline"});if e!=nil{t.Fatal(e)};pb,_:=providers.NewProcessBudget(5,0,0,1)
 role,e:=providers.BuildChatRole(ctx,providers.BuildConfig{Profile:p,APIKey:"offline",Budget:pb,Transport:providers.NewTransport(providers.WithHTTPClient(srv.Client()))});if e!=nil{t.Fatal(e)}
 saver:=&fakeMessageSaver{};return newTestChat(role,saver),saver,ctx
}
func TestReviewManagedChatCleanupOwnsFrontend(t *testing.T){for _,success:=range []bool{true,false}{t.Run(fmt.Sprint(success),func(t *testing.T){
 c,saver,ctx:=reviewManagedChat(t,!success);sr,cleanup,e:=c.GetAnswerStream(ctx,"session",nil,"question");if e!=nil{t.Fatal(e)}
 if success{for{_,e=sr.Recv();if e==io.EOF{break};if e!=nil{t.Fatal(e)}}}else{if _,e=sr.Recv();e!=nil{t.Fatal(e)}}
 done:=make(chan struct{});go func(){cleanup(StreamResult{Success:success});close(done)}();select{case<-done:case<-time.After(time.Second):t.Fatal("managed Chat cleanup hung")}
 if _,e=sr.Recv();!errors.Is(e,schema.ErrRecvAfterClosed){t.Errorf("cleanup left frontend Copy branch open: Recv error=%v",e)}
 saver.mu.Lock();assistants:=0;for _,r:=range saver.roles{if r==string(schema.Assistant){assistants++}};saver.mu.Unlock();want:=0;if success{want=1};if assistants!=want{t.Errorf("assistant saves=%d want=%d",assistants,want)}
})}}
func TestReviewSessionEarlyFrontendClose(t *testing.T){
 c,_,ctx:=reviewManagedChat(t,true);sr,cleanup,e:=c.GetAnswerStream(ctx,"session",nil,"question");if e!=nil{t.Fatal(e)};if _,e=sr.Recv();e!=nil{t.Fatal(e)}
 sr.Close()
 done:=make(chan struct{});go func(){cleanup(StreamResult{});close(done)}();select{case<-done:case<-time.After(time.Second):t.Fatal("early frontend cleanup hung")}
}
