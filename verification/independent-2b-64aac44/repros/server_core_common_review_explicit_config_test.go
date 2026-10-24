package common_test

import (
 "context"
 "testing"
 "github.com/cloudwego/eino-ext/components/model/openai"
 "github.com/wangle201210/go-rag/server/core/common"
 "github.com/wangle201210/go-rag/server/core/providers"
)

func TestSupplementExplicitChatConfigNotDiscarded(t *testing.T){
 old:=common.GetRegistry();defer common.SetRegistry(old)
 reg:=providers.NewFakeRegistry();common.SetRegistry(reg)
 got,err:=common.GetChatModel(context.Background(),&openai.ChatModelConfig{APIKey:"offline-key",BaseURL:"http://example.invalid/v1",Model:"explicit-distinct-model"})
 if err!=nil {return} // an explicit conflict error would also prevent silent misrouting
 if got==reg.Chat {t.Fatal("explicit model/endpoint config silently discarded in favor of Registry default")}
}
