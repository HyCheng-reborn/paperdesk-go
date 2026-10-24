package common_test
import (
 "context"
 "testing"
 "github.com/wangle201210/go-rag/server/core/common"
 "github.com/wangle201210/go-rag/server/core/providers"
)
func TestIndependentNestedEntryKeepsOwner(t *testing.T){
 old:=common.GetRegistry();defer common.SetRegistry(old)
 pb,_:=providers.NewProcessBudget(10,0,0,0);reg,err:=providers.NewRegistry(providers.RegistryConfig{Budget:pb,Credentials:func(context.Context,*providers.ProviderProfile)(string,error){return "offline",nil}});if err!=nil{t.Fatal(err)};common.SetRegistry(reg)
 ctx:=common.WithRequestBudget(context.Background(),providers.WithMaxRequests(1));rb:=providers.RequestBudgetFromContext(ctx);if rb==nil{t.Fatal("missing first owner")}
 lease,err:=rb.Reserve(&providers.TokenEstimate{InputTokens:1});if err!=nil{t.Fatal(err)};lease.Commit();lease.Finish(nil)
 child:=common.WithRequestBudget(ctx);next:=providers.RequestBudgetFromContext(child)
 if next!=rb{t.Error("nested entry replaced cumulative request owner")}
 if l,e:=next.Reserve(&providers.TokenEstimate{InputTokens:1});e==nil{l.Close();t.Error("nested entry refreshed exhausted request quota")}
}
func TestIndependentTaskUsesTransferredScope(t *testing.T){
 old:=common.GetRegistry();defer common.SetRegistry(old)
 pb,_:=providers.NewProcessBudget(10,0,0,0);reg,err:=providers.NewRegistry(providers.RegistryConfig{Budget:pb,Credentials:func(context.Context,*providers.ProviderProfile)(string,error){return "offline",nil}});if err!=nil{t.Fatal(err)};common.SetRegistry(reg)
 ctx,tb,budgetErr:=common.WithTaskBudget(context.Background(),providers.WithMaxRequests(1));if budgetErr!=nil{t.Fatal(budgetErr)};if tb==nil{t.Fatal("missing task")};rb:=providers.RequestBudgetFromContext(ctx)
 lease,err:=rb.Reserve(&providers.TokenEstimate{InputTokens:1});if err!=nil{t.Fatal(err)};lease.Commit();lease.Finish(nil)
 bg,err:=tb.TransferToBackground();if err!=nil{t.Fatal(err)}
 if bg.Snapshot().CommittedAttempts!=1{t.Errorf("transferred task lost actual synchronous usage: %+v",bg.Snapshot())}
 if l,e:=bg.Reserve(&providers.TokenEstimate{InputTokens:1});e==nil{l.Close();t.Error("background task quota refreshed because synchronous adapter used separate scope")}
}


