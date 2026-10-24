package providers

import (
 "context"
 "errors"
 "math"
 "testing"
 "time"
)

func TestReviewFix_ReservationOverflow(t *testing.T) {
 pb:=reviewProcessBudget(t,10,0,0,3)
 rb,err:=pb.NewRequestBudget(context.Background());if err!=nil {t.Fatal(err)}
 first,err:=rb.Reserve(&TokenEstimate{InputTokens:math.MaxInt64});if err!=nil {t.Fatal(err)};defer first.Close()
 before:=pb.Snapshot()
 second,reserveErr:=rb.Reserve(&TokenEstimate{InputTokens:math.MaxInt64})
 if second!=nil {defer second.Close()}
 after:=pb.Snapshot()
 if !errors.Is(reserveErr,ErrBudgetExhausted) || after!=before {t.Fatalf("unrepresentable reservation must be rejected atomically: before=%+v after=%+v error=%v",before,after,reserveErr)}
}

func TestReviewFix_ResolverCannotMutateRegistryIdentity(t *testing.T) {
 profile:=testChatProfile(t,"original-model")
 reg,err:=NewRegistry(RegistryConfig{Chat:profile,Budget:reviewProcessBudget(t,10,100,100,1),Credentials:func(_ context.Context,p *ProviderProfile)(string,error){p.Model="resolver-modified-model";return "offline-key",nil}})
 if err!=nil {t.Fatal(err)}
 role,err:=reg.GetChat(context.Background());if err!=nil {t.Fatal(err)}
 snapshot:=role.(interface{Profile()ProviderProfile}).Profile()
 if snapshot.Model!="original-model" {t.Fatalf("model published under original cache key was changed by credential callback: %q",snapshot.Model)}
}

func TestReviewFix_TaskRootOwnerSurvivesRequestCancel(t *testing.T) {
 root,stop:=context.WithCancel(context.Background());defer stop()
 request,cancelRequest:=context.WithCancel(context.Background());defer cancelRequest()
 pb:=reviewProcessBudget(t,10,100,100,1)
 tb,err:=pb.NewTaskBudget(root,WithDeadline(time.Hour));if err!=nil {t.Fatal(err)}
 syncLease,err:=tb.ReserveCtx(request,&TokenEstimate{InputTokens:1});if err!=nil {t.Fatal(err)};syncLease.Close()
 bg,err:=tb.TransferToBackground();if err!=nil {t.Fatal(err)}
 cancelRequest()
 next,err:=bg.ReserveCtx(root,&TokenEstimate{InputTokens:1});if err!=nil {t.Fatalf("service-root task owner inherited request cancellation: %v",err)};next.Close()
 stop()
 if l,err:=bg.Reserve(&TokenEstimate{InputTokens:1});!errors.Is(err,context.Canceled) {if l!=nil {l.Close()};t.Fatalf("root cancellation not observed: %v",err)}
}
