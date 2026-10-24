package rag
import (
 "context"
 "testing"
 v1 "github.com/wangle201210/go-rag/server/api/rag/v1"
 "github.com/cloudwego/eino/schema"
 "github.com/wangle201210/go-rag/server/core/common"
 "github.com/wangle201210/go-rag/server/core/providers"
 "github.com/wangle201210/go-rag/server/internal/logic/chat"
)
func TestIndependentControllerSharesOwnerAcrossStages(t *testing.T){
 old:=common.GetRegistry();defer common.SetRegistry(old)
 pb,_:=providers.NewProcessBudget(10,0,0,0);reg,err:=providers.NewRegistry(providers.RegistryConfig{Budget:pb,Credentials:func(context.Context,*providers.ProviderProfile)(string,error){return "offline",nil}});if err!=nil{t.Fatal(err)};common.SetRegistry(reg)
 var retrieveOwner *providers.RequestBudget
 sr:=schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok",nil)})
 c:=&ControllerV1{
 chatRetrieve:func(ctx context.Context,_ *v1.RetrieverReq)(*v1.RetrieverRes,error){ctx=common.WithRequestBudget(ctx);retrieveOwner=providers.RequestBudgetFromContext(ctx);return &v1.RetrieverRes{},nil},
 chatAnswerStream:func(ctx context.Context,_ string,_ []*schema.Document,_ string)(*schema.StreamReader[*schema.Message],func(chat.StreamResult),error){ctx=common.WithRequestBudget(ctx);answerOwner:=providers.RequestBudgetFromContext(ctx);if retrieveOwner==nil||answerOwner!=retrieveOwner{t.Error("actual ChatStream coordinator gave retrieval and answer different cumulative owners")};return sr,func(chat.StreamResult){sr.Close()},nil},
 chatSSEWrite:func(context.Context,*schema.StreamReader[*schema.Message],[]*schema.Document)error{return nil},
 }
 if _,err=c.ChatStream(context.Background(),&v1.ChatStreamReq{Question:"offline"});err!=nil{t.Fatal(err)}
}
