//go:build online
package probe_test
import("context";"errors";"testing";"time";"github.com/wangle201210/go-rag/server/core/probe";"github.com/wangle201210/go-rag/server/core/providers")
func TestBudgetReviewVersionedChatPassesPreflightThenSends(t *testing.T) {
 model:=probe.NewLocalModelServer();defer model.Close();behavior:=probe.DefaultModelBehavior();behavior.EmbedDims=8;model.SetBehavior(behavior)
 plan:=validPlan();plan.Chat.Endpoint=model.URL();plan.Embed.Endpoint=model.URL();plan.Rerank.URL=model.RerankURL();plan.Chat.Model="chat@v1";plan.Authorization=probe.Authorization{OnlineApproved:true,Scope:probe.ScopeS1,ApprovedBy:"offline",ApprovedAt:"offline"}
 steps:=[]probe.Step{{Kind:probe.StepEmbed,Texts:[]string{"offline"}},{Kind:probe.StepChat,Prompt:"offline"}};t.Setenv("STAGE2D_REVIEW_DUMMY_KEY","offline");cfg:=probe.OnlineConfig{Plan:plan,Credentials:probe.Credentials{EnvNames:[]string{"STAGE2D_REVIEW_DUMMY_KEY"}},Steps:steps}
 if !cfg.Ready(){t.Fatalf("expected erroneous readiness true: %+v",probe.Readiness(cfg))};if err:=probe.Preflight(plan,steps);err!=nil{t.Fatalf("preflight unexpectedly failed: %v",err)}
 report,err:=probe.RunOnline(context.Background(),cfg);if !errors.Is(err,providers.ErrModelVersionNotExpressible){t.Fatalf("err=%v",err)};if model.EmbedHits()!=1||model.ChatHits()!=0{t.Fatalf("embed=%d chat=%d report=%+v",model.EmbedHits(),model.ChatHits(),report)}
 t.Logf("OBSERVED Ready=true; Preflight=nil; embedding fake requests=%d; chat model version rejected after prior send",model.EmbedHits())
}

func TestBudgetReviewRetriesAfterAnotherStepStopsChain(t *testing.T) {
 gate:=probe.NewGate()
 h:=newProbeHarness(t,func(p *probe.Plan,b *probe.ModelBehavior){p.Retry=probe.RetryPolicy{MaxAttempts:2,RetryOnTransportError:true};p.Budget.MaxConcurrency=1;b.ChatGate=gate;b.ChatAbortFirst=1})
 ch:=make(chan error,1);go func(){_,err:=h.Chain.RunStep(context.Background(),probe.Step{Kind:probe.StepChat,Prompt:"offline"});ch<-err}()
 select{case <-gate.Reached():case <-time.After(3*time.Second):t.Fatal("fake model not reached")}
 _,err:=h.Chain.RunStep(context.Background(),probe.Step{Kind:probe.StepEmbed,Texts:[]string{"offline"}});if !errors.Is(err,providers.ErrBudgetExhausted){t.Fatalf("expected stop after exhaustion: %v",err)};before:=h.Model.ChatHits();gate.Release();err=<-ch
 if err!=nil||before!=1||h.Model.ChatHits()!=2{t.Fatalf("first err=%v hits before=%d after=%d",err,before,h.Model.ChatHits())};t.Logf("OBSERVED exhausted second step stops chain at hit=%d; in-flight first step retries and sends hit=%d",before,h.Model.ChatHits())
}

func TestBudgetReviewMutablePlanRace(t *testing.T) {
 h:=newProbeHarness(t,func(p *probe.Plan,b *probe.ModelBehavior){p.Budget.Unlimited=[]string{"requests"}})
 done:=make(chan struct{});go func(){defer close(done);for i:=0;i<1000;i++{h.Plan.Budget.Unlimited[0]="requests";h.Plan.Budget.Unlimited[0]="input_tokens"}}()
 for i:=0;i<100;i++{_,_=h.Chain.Run(context.Background(),[]probe.Step{{Kind:probe.StepChat,Prompt:"offline"}})};<-done
}
