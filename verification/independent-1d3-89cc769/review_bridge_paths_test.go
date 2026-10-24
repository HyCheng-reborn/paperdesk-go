package core

import (
 "context"
 "errors"
 "runtime"
 "sync/atomic"
 "testing"
 "time"
 "github.com/cloudwego/eino/components/document"
)

type reviewObservedRoot struct { context.Context; registrations atomic.Int32; stops atomic.Int32; bridgeStops atomic.Int32 }
func (c *reviewObservedRoot) Value(key any) any { return nil }
func (c *reviewObservedRoot) AfterFunc(f func()) func() bool {
 registration := c.registrations.Add(1)
 stop := context.AfterFunc(c.Context, f)
 return func() bool { ok := stop(); if ok { c.stops.Add(1); if registration == 1 { c.bridgeStops.Add(1) } }; return ok }
}
func TestReviewSyncPanicStopsCancellationBridge(t *testing.T) {
 r := newIndexerTestRag(&fakeSyncIndexer{onInvoke:func(context.Context,string,document.Source)([]string,error){panic("sync failure")}}, &fakeAsyncIndexer{})
 root := &reviewObservedRoot{Context:r.bgRootCtx}
 r.bgRootCtx = root
 defer func(){ctx,cancel:=context.WithTimeout(context.Background(),time.Second); defer cancel(); if err:=r.Shutdown(ctx);err!=nil{t.Error(err)}}()
 func(){defer func(){if recover()==nil{t.Error("expected synchronous panic")}}(); _,_ = r.Index(context.Background(), &IndexReq{URI:"fake",KnowledgeName:"kb",DocumentsId:42})}()
 t.Logf("registered bridges=%d; stopped bridges=%d; occupied slots=%d",root.registrations.Load(),root.stops.Load(),len(r.bgSem))
 if root.registrations.Load()!=root.stops.Load(){t.Error("synchronous panic retained cancellation bridge on service root")}
}


func TestReviewBridgeAllSyncExitPaths(t *testing.T) {
 for _,path:=range []string{"success","error","panic","goexit"} { t.Run(path,func(t *testing.T){
  r:=newIndexerTestRag(&fakeSyncIndexer{onInvoke:func(context.Context,string,document.Source)([]string,error){
   switch path {case "error":return nil,errors.New("sync error");case "panic":panic("sync panic");case "goexit":runtime.Goexit()}
   return []string{"id1"},nil
  }},&fakeAsyncIndexer{})
  r.asyncWait=func(context.Context){}
  root:=&reviewObservedRoot{Context:r.bgRootCtx};r.bgRootCtx=root
  finished:=make(chan struct{})
  go func(){defer close(finished);defer func(){_ = recover()}();_,_=r.Index(context.Background(),&IndexReq{URI:"fake",KnowledgeName:"kb",DocumentsId:42})}()
  select{case <-finished:case <-time.After(time.Second):t.Fatal("Index did not exit")}
  if root.registrations.Load()<1 || root.bridgeStops.Load()!=1 {t.Errorf("registered=%d stopped=%d",root.registrations.Load(),root.stops.Load())}
  ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel()
  if err:=r.Shutdown(ctx);err!=nil{t.Fatal(err)}
  if len(r.bgSem)!=0{t.Error("capacity not released")}
 }) }
}

