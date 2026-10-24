# 来源与可复核位置

目标 a24cb8d609e6c4580b0726cb7de0ccd21d2efdd1；核实日期2026-10-19。只访问文档，没有调用模型API。

## 官方资料

- https://help.aliyun.com/zh/model-studio/compatibility-of-openai-with-dashscope ：Chat OpenAI兼容端点、各地域baseURL；新workspace域名与旧域名兼容说明。用于确认显式Chat endpoint的语义，不据此证明实际区域调用成功。
- https://help.aliyun.com/en/model-studio/rerank ：qwen3-rerank支持OpenAI兼容 /compatible-api/v1/reranks，示例使用扁平model/query/documents/top_n。用于确认本批Rerank协议选择可以成立，区分Chat路径和排序路径。
- https://help.aliyun.com/zh/model-studio/text-rerank-api ：DashScope原生排序使用嵌套input/parameters；不与兼容协议混用。
- https://help.aliyun.com/zh/model-studio/text-embedding-synchronous-api ：向量响应的data/index与usage结构。实际index校验能力另由锁定SDK源码与独立httptest验证。

以上均为离线设计/协议资料核实，不视为真实服务能力、可用性或计费探针。

## 锁定模块源码

- C:/Users/zhy/go/pkg/mod/github.com/cloudwego/eino@v0.6.0/components/model/interface.go ：Generate/Stream与schema泛型reader签名。
- C:/Users/zhy/go/pkg/mod/github.com/cloudwego/eino@v0.6.0/components/embedding/interface.go ：EmbedStrings签名。
- C:/Users/zhy/go/pkg/mod/github.com/cloudwego/eino-ext/libs/acl/openai@v0.1.3/embedding.go:151 ：调用CreateEmbeddings后，按resp.Data数组顺序复制Embedding，不读d.Index；usage转为无presence的数值字段。
- 同模块chat_model.go:734/824 ：Generate/Stream进入底层SDK构造请求；HTTPClient配置委托。
- C:/Users/zhy/go/pkg/mod/github.com/meguminnnnnnnnn/go-openai@v0.1.0/client.go:138 ：sendRequest调用一次HTTPClient.Do、defer关闭body、再解码；stream方法将body关闭交给流。

## 生产装配与复现

- server/internal/cmd/cmd.go:50 ：dao→rag→chat初始化；logic/rag导入bailian使init注册执行。
- server/internal/logic/rag/retriever.go:74 ：构造并提前SetRegistry；:100 buildRegistry；:120 无限新预算；:180 Embedding配置；:202 Rerank URL派生；:395 provider读取。
- server/core/rag.go:98/106/117/121/129 ：实际组件构造、QA和Chat入口；core/retriever.go:76 Rewrite、:255旧Rerank。
- server/core/indexer/indexer.go:19、indexer_async.go:18、retriever/retriever.go:25 ：共享NewEmbedding入口，故启用门禁必须覆盖它。
- server/core/providers/registry.go:221/:261/:296 ：builder选择；:340–358 无锁可变注册表。
- 本目录repros/*.go为补充测试完整来源；*.go.diff为审查新增diff，repro-hashes.txt记录SHA256；production的git diff保持为空。
