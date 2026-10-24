package rag

import (
 "context"
 "errors"
 "reflect"
 "sync"
 "testing"
 "time"
 "unsafe"
 "github.com/wangle201210/go-rag/server/core"
)

func TestReviewResetTimeoutThenRetryRealRegistration(t *testing.T) {
 defer resetState()
 r:=&core.Rag{}
 root,cancel:=context.WithCancel(context.Background())
 defer cancel()
 v:=reflect.ValueOf(r).Elem()
 for name,value:=range map[string]any{"bgRootCtx":root,"bgRootCancel":cancel,"done":make(chan struct{})} {
  f:=v.FieldByName(name); reflect.NewAt(f.Type(),unsafe.Pointer(f.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
 }
 field:=v.FieldByName("bgWG")
 registration:=reflect.NewAt(field.Type(),unsafe.Pointer(field.UnsafeAddr())).Interface().(*sync.WaitGroup)
 registration.Add(1)
 var release sync.Once
 defer release.Do(registration.Done)
 store:=&fakeVectorStore{}
 ragMu.Lock();ragSvr=r;vectorStore=store;ragInit=true;ragMu.Unlock()
 finished:=make(chan error,1)
 go func(){finished<-Reset()}()
 select{case <-root.Done():case <-time.After(time.Second):t.Fatal("Reset did not stop root")}
 lookup:=make(chan *core.Rag,1)
 go func(){lookup<-GetRagSvr()}()
 select{case got:=<-lookup:if got!=r{t.Error("waiting instance changed")};case <-time.After(time.Second):t.Fatal("lookup blocked during wait")}
 select{case err:=<-finished:if !errors.Is(err,context.DeadlineExceeded){t.Fatalf("expected identifiable timeout, got %v",err)};case <-time.After(7*time.Second):t.Fatal("Reset did not honor timeout")}
 ragMu.RLock();preserved:=ragSvr==r && vectorStore==store && ragInit;ragMu.RUnlock()
 store.mu.Lock();closed:=store.closeCount;store.mu.Unlock()
 if !preserved || closed!=0{t.Fatalf("timeout did not preserve exact objects: preserved=%v closeCount=%d",preserved,closed)}
 release.Do(registration.Done)
 if err:=Reset();err!=nil{t.Fatal(err)}
 if err:=Reset();err!=nil{t.Fatal(err)}
 store.mu.Lock();closed=store.closeCount;store.mu.Unlock()
 if closed!=1{t.Fatalf("expected exactly one Close, got %d",closed)}
 if IsInitialized() || GetRagSvr()!=nil{t.Fatal("completed Reset retained published state")}
}
