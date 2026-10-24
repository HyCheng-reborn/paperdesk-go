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
 "github.com/wangle201210/go-rag/server/core/common"
 "github.com/wangle201210/go-rag/server/core/providers"
)

func TestSupplementResetTimeoutPreservesRegistry(t *testing.T){
 old:=common.GetRegistry();reg:=providers.NewFakeRegistry();common.SetRegistry(reg)
 target:=&core.Rag{};root,cancel:=context.WithCancel(context.Background())
 field:=reflect.ValueOf(target).Elem().FieldByName("bgWG");wg:=reflect.NewAt(field.Type(),unsafe.Pointer(field.UnsafeAddr())).Interface().(*sync.WaitGroup);wg.Add(1)
 setRagField(target,"bgRootCtx",root);setRagField(target,"bgRootCancel",cancel);setRagField(target,"bgSem",make(chan struct{},8));setRagField(target,"done",make(chan struct{}))
 ragMu.Lock();ragSvr=target;vectorStore=&fakeVectorStore{};ragInit=true;ragMu.Unlock()
 t.Cleanup(func(){wg.Done();select{case<-ragDoneChan(target):case<-time.After(2*time.Second):t.Error("actual Shutdown done not closed")};cancel();resetState();common.SetRegistry(old)})
 err:=Reset()
 if !errors.Is(err,context.DeadlineExceeded) {t.Fatalf("fixture did not reach real shutdown timeout: %v",err)}
 if common.GetRegistry()!=reg {t.Fatal("shutdown timed out with dependencies preserved, but Registry was cleared before wait")}
}
