//go:build online
package probe_test
import("context";"testing";"errors";"github.com/wangle201210/go-rag/server/core/probe";"github.com/wangle201210/go-rag/server/core/providers")
func TestReviewOnlineDirectChainSkipsAuthorization(t *testing.T){
 plan:=validPlan();plan.Chat.Endpoint="https://review.example.invalid/v1";plan.Embed.Endpoint="https://review.example.invalid/v1";plan.Rerank.URL="https://review.example.invalid/rerank"
 if plan.Authorization.OnlineApproved {t.Fatal("fixture should be unauthorized")}
 target,err:=probe.NewLocalFakeESTarget(plan.Target,plan.Embed.Model,plan.Embed.Dimensions);if err!=nil{t.Fatal(err)};defer target.Close()
 chain,err:=probe.NewChain(context.Background(),plan,target,probe.ChainOptions{APIKey:"offline-fake-key"});if err!=nil{t.Fatalf("unauthorized NewChain rejected: %v",err)};defer chain.Close()
 before:=onlineProxyHits.Load();report,err:=chain.RunStep(context.Background(),probe.Step{Kind:probe.StepChat,Prompt:"offline"}); delta:=onlineProxyHits.Load()-before
 if delta!=1 || report.CommittedAttempts!=1 || err==nil{t.Fatalf("expected one local fake proxy CONNECT (502) and one commit: proxy=%d report=%+v err=%v",delta,report,err)}
 t.Logf("OBSERVED unauthorized direct online NewChain + RunStep started HTTPS CONNECT to local fake proxy, commits=%d; fake proxy rejected CONNECT, no real service contacted",report.CommittedAttempts)
}

func TestReviewReadinessAcceptsEmptyCredential(t *testing.T){
 plan:=validPlan();plan.Authorization=probe.Authorization{OnlineApproved:true,Scope:probe.ScopeS1,ApprovedBy:"offline",ApprovedAt:"offline"};t.Setenv("PROBE_REVIEW_EMPTY","")
 cfg:=probe.OnlineConfig{Plan:plan,Credentials:probe.Credentials{EnvNames:[]string{"PROBE_REVIEW_EMPTY"}},Steps:[]probe.Step{{Kind:probe.StepChat,Prompt:"offline"}}}
 if !cfg.Ready(){t.Fatalf("expected observed ready=true: %+v",probe.Readiness(cfg))};_,err:=probe.RunOnline(context.Background(),cfg);if !errors.Is(err,probe.ErrMissingCredentials){t.Fatalf("expected missing credential: %v",err)};t.Log("OBSERVED empty credential: Ready() true, RunOnline rejects before any network")
}

func TestReviewPreflightAllowsVersionedLaterRerank(t *testing.T){
 model:=probe.NewLocalModelServer();defer model.Close();p:=validPlan();p.Chat.Endpoint=model.URL();p.Embed.Endpoint=model.URL();p.Rerank.URL=model.RerankURL();p.Rerank.Model="m@v1";p.Authorization=probe.Authorization{OnlineApproved:true,Scope:probe.ScopeS1,ApprovedBy:"offline",ApprovedAt:"offline"};t.Setenv("PROBE_REVIEW_DUMMY","offline")
 cfg:=probe.OnlineConfig{Plan:p,Credentials:probe.Credentials{EnvNames:[]string{"PROBE_REVIEW_DUMMY"}},Steps:[]probe.Step{{Kind:probe.StepChat,Prompt:"offline"},{Kind:probe.StepRerank,Query:"offline",TopK:1}}}
 if !cfg.Ready(){t.Fatalf("expected observed ready: %+v",probe.Readiness(cfg))};_,err:=probe.RunOnline(context.Background(),cfg);if !errors.Is(err,providers.ErrModelVersionNotExpressible)||model.ChatHits()!=1||model.RerankHits()!=0{t.Fatalf("err=%v chat=%d rerank=%d",err,model.ChatHits(),model.RerankHits())};t.Log("OBSERVED preflight accepts unsupported rerank version: one chat already sent before rejection")
}
