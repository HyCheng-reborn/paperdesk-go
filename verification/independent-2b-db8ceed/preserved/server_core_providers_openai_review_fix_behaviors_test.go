package openai_test

import (
 "context"
 "encoding/json"
 "errors"
 "net/http"
 "net/http/httptest"
 "sync"
 "sync/atomic"
 "testing"
 "time"

 "github.com/cloudwego/eino/components/model"
 "github.com/cloudwego/eino/schema"
 "github.com/wangle201210/go-rag/server/core/providers"
 "github.com/wangle201210/go-rag/server/core/providers/bailian"
 _ "github.com/wangle201210/go-rag/server/core/providers/openai"
)

func supplementProfile(t *testing.T,url,provider string,dim int)*providers.ProviderProfile{
 t.Helper();p,err:=providers.NewProviderProfile(providers.ProviderProfile{ProviderName:provider,Protocol:providers.ProtocolOpenAICompat,Endpoint:url,Model:"test-model",Dimensions:dim});if err!=nil {t.Fatal(err)};return p
}

func TestSupplementStreamEarlyCloseFinalizes(t *testing.T){
 release:=make(chan struct{});var once sync.Once;handlerDone:=make(chan struct{})
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){defer close(handlerDone);w.Header().Set("Content-Type","text/event-stream");_,_=w.Write([]byte("data: {\"id\":\"s\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"one\"}}]}\n\n"));w.(http.Flusher).Flush();select{case<-release:case<-r.Context().Done():}}))
 pb,err:=providers.NewProcessBudget(10,0,0,1);if err!=nil {t.Fatal(err)}
 ctx,cancel:=context.WithCancel(context.Background())
 t.Cleanup(func(){cancel();once.Do(func(){close(release)});select{case<-handlerDone:case<-time.After(2*time.Second):t.Error("HTTP handler did not stop")};srv.Close();deadline:=time.NewTimer(2*time.Second);defer deadline.Stop();tick:=time.NewTicker(time.Millisecond);defer tick.Stop();for pb.Snapshot().InFlight!=0{select{case<-tick.C:case<-deadline.C:t.Error("budget slot did not clean up after explicit cancel");return}}})
 role,err:=bailian.BuildChat(ctx,providers.BuildConfig{Profile:supplementProfile(t,srv.URL,"bailian",0),APIKey:"offline-key",Transport:providers.NewTransport(providers.WithHTTPClient(srv.Client())),Budget:pb});if err!=nil {t.Fatal(err)}
 sr,err:=role.Stream(ctx,[]*schema.Message{schema.UserMessage("hello")});if err!=nil {t.Fatal(err)}
 if _,err=sr.Recv();err!=nil {t.Fatal(err)}
 sr.Close()
 deadline:=time.NewTimer(100*time.Millisecond);defer deadline.Stop();tick:=time.NewTicker(time.Millisecond);defer tick.Stop()
 for pb.Snapshot().InFlight!=0 {select{case<-tick.C:case<-deadline.C:t.Fatalf("consumer Close left lease active until external cancellation: %+v",pb.Snapshot())}}
}

func TestSupplementOutputOptionAboveReservation(t *testing.T){
 var sentMax atomic.Int64
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){var body struct{MaxTokens int `json:"max_tokens"`};if err:=json.NewDecoder(r.Body).Decode(&body);err!=nil {t.Error(err)};sentMax.Store(int64(body.MaxTokens));w.Header().Set("Content-Type","application/json");_,_=w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":4097,"total_tokens":4098}}`))}));defer srv.Close()
 pb,err:=providers.NewProcessBudget(10,0,8192,1);if err!=nil {t.Fatal(err)}
 role,err:=bailian.BuildChat(context.Background(),providers.BuildConfig{Profile:supplementProfile(t,srv.URL,"bailian",0),APIKey:"offline-key",Transport:providers.NewTransport(providers.WithHTTPClient(srv.Client())),Budget:pb});if err!=nil {t.Fatal(err)}
 _,err=role.Generate(context.Background(),[]*schema.Message{schema.UserMessage("hello")},model.WithMaxTokens(16384))
 if errors.Is(err,providers.ErrBudgetExhausted) {return}
 if err!=nil {t.Fatal(err)}
 if sentMax.Load()>8192||sentMax.Load()==0 {t.Fatalf("sent output limit did not match reserved maximum: sentMax=%d snapshot=%+v",sentMax.Load(),pb.Snapshot())}
}

func TestSupplementSupportedOpenAIBuilder(t *testing.T){
 pb,err:=providers.NewProcessBudget(10,0,0,1);if err!=nil {t.Fatal(err)}
 reg,err:=providers.NewRegistry(providers.RegistryConfig{Chat:supplementProfile(t,"http://example.invalid/v1","openai",0),Budget:pb,Credentials:func(context.Context,*providers.ProviderProfile)(string,error){return "offline-key",nil}});if err!=nil {t.Fatal(err)}
 if _,err=reg.GetChat(context.Background());err!=nil {t.Fatalf("declared supported OpenAI provider has no production builder: %v",err)}
}

func TestSupplementEmbeddingDefaultRoundTripper(t *testing.T){
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Header().Set("Content-Type","application/json");_,_=w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`))}));defer srv.Close()
 defer func(){if p:=recover();p!=nil {t.Errorf("valid injected http.Client with nil Transport caused panic: %v",p)}}()
 role,err:=bailian.BuildEmbed(context.Background(),providers.BuildConfig{Profile:supplementProfile(t,srv.URL,"bailian",1),APIKey:"offline-key",Transport:providers.NewTransport(providers.WithHTTPClient(&http.Client{Timeout:time.Second}))});if err!=nil {t.Fatal(err)}
 if _,err=role.EmbedStrings(context.Background(),[]string{"one"});err!=nil {t.Fatal(err)}
}


func TestSupplementRerankNilDocPreservesError(t *testing.T){
 defer func(){if p:=recover();p!=nil {t.Errorf("Stage1 nil-document error became estimator panic: %v",p)}}()
 role,err:=bailian.BuildRerank(context.Background(),providers.BuildConfig{Profile:supplementProfile(t,"http://example.invalid/reranks","bailian",0),APIKey:"offline-key",Transport:providers.NewTransport()});if err!=nil {t.Fatal(err)}
 if _,err=role.Rerank(context.Background(),"query",[]*schema.Document{nil},1);err==nil {t.Error("nil document must return error")}
}

func TestSupplementInvalidInputDoesNotSpendAttempt(t *testing.T){
 pb,err:=providers.NewProcessBudget(1,100,0,1);if err!=nil {t.Fatal(err)}
 role,err:=bailian.BuildRerank(context.Background(),providers.BuildConfig{Profile:supplementProfile(t,"http://example.invalid/reranks","bailian",0),APIKey:"offline-key",Transport:providers.NewTransport(),Budget:pb});if err!=nil {t.Fatal(err)}
 // Existing production Rerank validates topK before HTTP; endpoint is never contacted.
 _,err=role.Rerank(context.Background(),"query",[]*schema.Document{{Content:"doc"}},0)
 if err==nil {t.Fatal("invalid topK must fail before sending")}
 if snap:=pb.Snapshot();snap.AttemptsUsed!=0||snap.InputUsed!=0 {t.Fatalf("pre-send validation failure spent committed budget: %+v",snap)}
}

func TestSupplementRedirectCountsHTTPAttempts(t *testing.T){
 var sends atomic.Int64
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){sends.Add(1);if r.URL.Path!="/finish" {w.Header().Set("Location","/finish");w.WriteHeader(http.StatusTemporaryRedirect);return};w.Header().Set("Content-Type","application/json");_,_=w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))}));defer srv.Close()
 pb,err:=providers.NewProcessBudget(1,0,0,1);if err!=nil {t.Fatal(err)}
 role,err:=bailian.BuildChat(context.Background(),providers.BuildConfig{Profile:supplementProfile(t,srv.URL,"bailian",0),APIKey:"offline-key",Transport:providers.NewTransport(providers.WithHTTPClient(srv.Client())),Budget:pb});if err!=nil {t.Fatal(err)}
 _,callErr:=role.Generate(context.Background(),[]*schema.Message{schema.UserMessage("hello")})
 if sends.Load()>1 {t.Fatalf("redirect bypassed actual-attempt limit: sends=%d callError=%v snapshot=%+v",sends.Load(),callErr,pb.Snapshot())}
}

func TestSupplementEmbeddingSettlesKnownUsage(t *testing.T){
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Header().Set("Content-Type","application/json");_,_=w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":100,"total_tokens":100}}`))}));defer srv.Close()
 pb,err:=providers.NewProcessBudget(10,10,0,1);if err!=nil {t.Fatal(err)}
 role,err:=bailian.BuildEmbed(context.Background(),providers.BuildConfig{Profile:supplementProfile(t,srv.URL,"bailian",1),APIKey:"offline-key",Transport:providers.NewTransport(providers.WithHTTPClient(srv.Client())),Budget:pb});if err!=nil {t.Fatal(err)}
 if _,err=role.EmbedStrings(context.Background(),[]string{"hi"});err!=nil {t.Fatal(err)}
 if snap:=pb.Snapshot();snap.InputUsed!=100 {t.Fatalf("known response usage ignored in favor of estimate: %+v",snap)}
}

func TestIndependentOpenAIStreamEarlyCloseFinalizes(t *testing.T){
 release:=make(chan struct{});var once sync.Once;handlerDone:=make(chan struct{})
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){defer close(handlerDone);w.Header().Set("Content-Type","text/event-stream");_,_=w.Write([]byte("data: {\"id\":\"s\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"one\"}}]}\n\n"));w.(http.Flusher).Flush();select{case<-release:case<-r.Context().Done():}}))
 pb,err:=providers.NewProcessBudget(10,0,0,1);if err!=nil {t.Fatal(err)}
 ctx,cancel:=context.WithCancel(context.Background())
 t.Cleanup(func(){cancel();once.Do(func(){close(release)});select{case<-handlerDone:case<-time.After(2*time.Second):t.Error("HTTP handler did not stop")};srv.Close();deadline:=time.NewTimer(2*time.Second);defer deadline.Stop();tick:=time.NewTicker(time.Millisecond);defer tick.Stop();for pb.Snapshot().InFlight!=0{select{case<-tick.C:case<-deadline.C:t.Error("budget slot did not clean up after explicit cancel");return}}})
 role,err:=providers.BuildChatRole(ctx,providers.BuildConfig{Profile:supplementProfile(t,srv.URL,"openai",0),APIKey:"offline-key",Transport:providers.NewTransport(providers.WithHTTPClient(srv.Client())),Budget:pb});if err!=nil {t.Fatal(err)}
 sr,err:=role.Stream(ctx,[]*schema.Message{schema.UserMessage("hello")});if err!=nil {t.Fatal(err)}
 if _,err=sr.Recv();err!=nil {t.Fatal(err)}
 sr.Close()
 deadline:=time.NewTimer(100*time.Millisecond);defer deadline.Stop();tick:=time.NewTicker(time.Millisecond);defer tick.Stop()
 for pb.Snapshot().InFlight!=0 {select{case<-tick.C:case<-deadline.C:t.Fatalf("consumer Close left lease active until external cancellation: %+v",pb.Snapshot())}}
}



