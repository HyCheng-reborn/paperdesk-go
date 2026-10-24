package common_test

import (
 "context"
 "testing"

 "github.com/cloudwego/eino-ext/components/model/openai"
 "github.com/wangle201210/go-rag/server/core/common"
 "github.com/wangle201210/go-rag/server/core/providers"
)

func TestReviewLegacyCacheDoesNotOverrideRegistry(t *testing.T){
 old:=common.GetRegistry();defer common.SetRegistry(old);common.SetRegistry(nil)
 legacy,err:=common.GetChatModel(context.Background(),&openai.ChatModelConfig{APIKey:"offline-key",BaseURL:"http://example.invalid/v1",Model:"legacy-model"});if err!=nil {t.Fatal(err)}
 reg:=providers.NewFakeRegistry();common.SetRegistry(reg)
 got,err:=common.GetChatModel(context.Background(),nil);if err!=nil {t.Fatal(err)}
 if got==legacy||got!=reg.Chat {t.Fatal("previous legacy singleton overrides newly enabled provider Registry")}
}
