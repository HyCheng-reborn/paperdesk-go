package bailian
import (
 "context"
 "io"
 "testing"
 "github.com/cloudwego/eino/schema"
 "github.com/wangle201210/go-rag/server/core/providers"
)
func TestIndependentStreamPreservesChunks(t *testing.T){
 pb,_:=providers.NewProcessBudget(0,0,0,1);rb,_:=pb.NewRequestBudget(context.Background());lease,_:=rb.Reserve(&providers.TokenEstimate{InputTokens:1,MaxOutput:1});lease.Commit()
 source:=schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("first",nil),schema.AssistantMessage("second",nil),schema.AssistantMessage("third",nil)})
 wrapped:=wrapLeaseStreamReader(source,lease);defer wrapped.Close()
 count:=0
 for {_,err:=wrapped.Recv();if err==io.EOF{break};if err!=nil{t.Fatal(err)};count++}
 if count!=3{t.Fatalf("production wrapper silently truncated source: got=%d want=3",count)}
}
