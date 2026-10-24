**结论：修改后可开始首批；目前仍有两项直接影响 2A 预算核心正确性的阻塞。接口和缓存方案已有实质改善，无需再整体重写方案，也无需等待在线探针。**

完整目标：0d0bab9e966065dbd6e41528bd5291c9a5c6f30f。
本轮增量起点：3bde7cd881672d54b99aef0cd8d02a27cc635d6d；原 Stage 2 基线 69984c4038d3bc04f822bdc6e31e40af73e3dc44 仍为祖先。

沿用已确认的只读设计复核标准，复用 C:/Zhy/code/go-rag-review-1d3 linked worktree，切换前确认干净。三份增量文件都是文档。原始临时测试、历史报告与证据保留；本轮未修改方案或业务代码、未运行测试/模型 API/真实设施，未提交、推送或更新验收状态。

**前次 A1–A3 的修订判断**

| 项目 | 本轮判断 |
|---|---|
| A1 预算语义 | 部分修复：发送后失败不退款、保守保留 token、禁止 stream 重放、进程 owner、一次性 lease 的方向正确；发送前尝试额度准入和 slot 生命周期仍错误，见 R1/R2 |
| A2 锁定 Eino 接口 | 原阻塞已闭合：Stream 改为 schema 泛型 reader，Generate/Stream 带 model.Option，EmbedStrings 与 Eino 对齐，GetExtract/capabilities 已补。model.TokenUsage 也确实存在于锁定 components/model/callback_extra.go:25，不误报不存在 |
| A3 共享核心、缓存与默认接线分批 | 首批方向基本闭合：2A 明确 Profile＋共享预算/适配核心，2B 承接默认工厂；非 nil 配置参与身份、ProcessBudget 不因新 Registry 重置。保留少量映射/配置结构澄清建议，不另列首批阻塞 |

**R1／高，阻塞 2A：MaxRequests 未在发送前预留，请求/任务 owner 仍未形成可执行的累计上限。**

方案位置：§4.1 行 221、§4.4 行 252–257、§4.9 行 293–296、§7.1 行 467–471。

当前发送前的步骤只有占并发 slot、预留 token；尝试数在发送后才消耗。即便不再退款，也不能在发出前阻止超额发送。最小反例：MaxRequests=1、MaxConcurrency=2、token 足够，两个 goroutine 同时通过上述步骤，各发一次，再在发送后扣减。两次都已实际发送，第二次扣减或报错无法挽回。只有“发送前预留机制”的标题不足以弥补步骤中缺少尝试额度准入。

同时，§4.3 说 RequestBudget 覆盖整个 Retrieve/Index，§7.1 的实际草案却是 ProcessBudget.NewAttempt(ctx) 返回一个 RequestBudget，只暴露 Finish/Abort；没有明确独立的 Request/TaskBudget owner、其累计上限和每 attempt 估算额度从何而来。若通过 ctx 提供，需要写明其必填契约和缺失行为，不能让每次尝试新建一份满额“请求预算”。§4.9 只写后台从 ProcessBudget 获取额度，没有写该 Index 的同步＋后台累计受哪个 task 上限约束。只保留进程上限不等于单任务上限。

实际源码证明该交接必须显式设计：core/indexer.go:164 的后台 context 从服务 root 派生，不继承请求 value；controller ChatStream 又在同一请求内先 Retrieve、再 answerStream。仅在子步骤 context 内创建独立预算会漏掉后台或最终答案。

最小修正：
- 构造可累计的 Request/TaskBudget，绑定独立 ProcessBudget owner；每 attempt 先在两个作用域整体预留“1 次尝试＋输入估算＋最大输出＋并发 slot”，任一不足都不进入 sender。
- 区分 reserved attempt 与 committed/sent attempt：进入实际发送边界前原子承诺，承诺后网络失败不退。仅能证明未发送的路径撤销预留。不能等响应或发送完成才尝试扣最后一个额度。
- 用 AttemptLease 承载每次生命周期，Request/TaskBudget 不作为一次尝试的返回对象混用；具体类型名可不同，但 owner/累计额度/估算参数/发送状态必须可表达。
- 明确 Index 同步与后台共享同一个累计 task owner，或显式划分子额度且总和受单任务上限约束；移交 owner 不移交 HTTP 取消，保留 Stage 1 后台生命周期。
- Chat 检索、改写、Rerank 与最终生成使用同一请求 owner，不为每步重新给满额。

必要离线验收：MaxRequests=1 的并发发送计数实际为 1；请求预算足够而进程不足以及相反情况均零发送且完全回退；多次重试/子步骤/后台接管不能刷新累计额度；发送后网络失败不返还尝试计数；重复 Finish/Abort/Close 不重复退款/结算。

本项不是要求提供完整代码或特定锁实现，只要求方案给出不会允许超发的状态机及构造关系。尚未解决前不宜开始 budget.go 实施。

**R2／高，阻塞 2A：slot 的释放点过早或无法在中断路径收敛。**

方案位置：§4.1 行 222、§4.5 行 266、§4.7 行 281–282。

非流式规定 HTTPClient.Do 返回即释放 slot。标准 net/http.Client.Do 返回 Response 和仍需消费/关闭的 Body，不代表 body 已读完、JSON 已解析或请求处理已终结。慢速非流式 body 仍在读取时，其他尝试会被继续放行，MaxConcurrency 实际只限制响应头阶段，不能限制完整在途请求。usage 也尚未解析，slot 释放与“attempt 结束时一次性结算”的定义冲突。

流式规定“body EOF + stream.Close 后”才释放，但用户主动关闭、ctx 取消或读取失败可能永远没有 EOF。若按两个条件都满足实现，slot 会永久占用，进程预算在若干中断后耗尽。不能为释放槽而强求把已取消 stream drain 到 EOF。方案一处写 EOF/Close，一处明确写“+”，该区别关系资源收敛，不是普通措辞问题。

源码证据：本地官方 Go 源码 C:/Program Files/Go/src/net/http/client.go 的 Client.Do 文档（约 554–580）要求调用者继续读取并关闭 Response.Body；SDK go-openai@v0.1.0/client.go:138–164 也是先 Do，再解码并关闭 body，stream 在 stream.Close 路径关闭底层 body。Stage 1 chat 的 coordinator 在前台失败时主动取消/Close，这正是不能要求正常 EOF 的生产路径。

最小修正：
- 非流式以 body 消费/解析成功并关闭，或读错/取消后关闭，作为 attempt 终结；Do 在尚无可用响应时失败，则走对应错误终结。
- 流式正常 EOF、终止性读取错误、取消后关闭、调用方提前 Close 都能走同一个幂等 finalize；完成必要取消/关闭后释放 slot，不要求同时观察 EOF 与 Close。
- slot 释放与 token 结算策略区分：流失败可释放在途槽但保守保留 token 额度；两者均只结算一次，不能保留 token 就连 slot 一并泄漏。
- 明确透明包装 reader/body 的所有权，不与消费者抢读，不新增裸 goroutine 包装阻塞调用；不改变 Stage 1 SSE EOF/错误和流关闭规则。

必要离线验收：响应头已到而 body 被 httptest 阻塞时，下个请求不能越过并发上限；非流式读取失败、stream 提前 Close/取消/错误均最终释放 slot；Close 与 EOF 并发只 finalize 一次；unknown usage 不退款 token，但下一请求仍能使用释放的 slot。

**已改善的部分与非阻塞建议**

- Eino 接口草案现可与锁定 method set 对齐。TokenUsage 同时有 model callback 类型和 schema 类型，实际适配时做显式转换并保留 presence 即可，不是当前编译型阻塞。
- Profile identity 字段和 nil/非 nil 配置处理已覆盖前次主要缓存问题。ProviderProfile.KeyOptions 是 map，构造时需防御性复制、缓存键需稳定归一化；公开字段不能仅凭注释保证不可变，列入 2A 配置隔离测试即可。
- 2B 已明确接管 Chat factory。映射表仍把 internal/logic/chat/chat.go 的 factory 写成 core/rag.go x.cm 的创建来源，实际它服务独立 Chat 单例，Rag.New 另调用 common.GetChatModel；Rewrite/Rerank 的 core/retriever.go 路径也仍误写成 core/retriever/retriever.go。实施接线时按真实两个 Chat 入口核对，不能据错误表格漏接最终答案。这些在前轮源码证据已定位，2A 共享核心不依赖提前修完整生产图。
- §3.3 仍说 2A 改兼容层，2A 文件清单又只有新增，2B 才列兼容层。当前 2A 目标可合理限定为新增共享构造/适配/预算核心，不宣称全应用已受限；若实改兼容层，回滚应包含引用者，不能只删 providers 目录。不据这些一般范围表述再开额外阻塞。
- Capabilities.StructuredOutput 和 ToolCalling 不应互相代替；可分别标 JSON object/JSON Schema/tool calling，或保留最小布尔但定义清楚，不要求现在探针所有能力。

**仍只阻塞后续批次的原 B1–B3**

- B1（2B）：方案已正确承接“原始响应边界校验”，不再要求在已丢 index 的 SDK 返回值上事后校验；实际 raw decoder/usage presence 的具体实现须在 2B 前落实，不拦 2A。
- B2（2C/生产启用）：仍仅写 fingerprint 存取及旧索引策略“必须承接”，没有冻结后端记录/读取和缺失 fingerprint 的 fail-closed 规则；应在 2C 前细化。新增列表补了 config/helpers 和 Qdrant indexer，却把原 vector/es.go、vector/qdrant.go 和 common/es.go 的维度位置从“全部表”漏掉，实施时仍按真实可达链覆盖，不要求首批改这些文件。
- 尤其 2B 明确写“新 Embedding profile 生产启用”，而保护它的 guard 在 2C。必须让生产启用等待 2C guard（或将必要 guard 前移）；2B 可先交付离线适配，不能在 guard 就绪前向旧空间写入不同模型向量。该门禁只阻塞生产启用，不阻塞 2A，也不要求先启动向量库。
- B3（2B）：仍需选定独立协议 encoder/decoder 与共享 Rerank 校验/输出实现；不能把“复用校验链”当作已设计完成。保持已有输入隔离、限长、超时与错误链。

官方协议/能力核实沿用前轮同日官方文档及锁定源码；本次没有新的 Provider 协议事实变更，无需重复浏览或在线验证。gte-rerank 退役日期、真实账号模型可用性、usage、schema能力仍按前轮边界处理。

**推荐下一步与首批验收**

只需专家模式集中修订 R1/R2 以及直接关联的预算 owner/API 草案和测试条件，不必重写已修正的角色接口、缓存设计，也不要开始编码。上述两项修正后，可开始限定的 2A：不可变 Profile/Role、共享预算执行状态机、实际 Eino 适配和明确构造入口；不含新模型生产启用、fingerprint 全量迁移或在线探针。

2A 验收应以实际 fake sender/httptest 经同一生产共享预算核心验证 R1/R2，而非只断言 fake 自身。预算字段不得通过模型 options 在发送前被覆盖绕过；输出 cap 实际进入请求。异常 usage 保守结算、超估算实际 usage 不产生可重复释放额度；失败初始化不发布缓存；配置/凭据身份隔离；stream 关闭保持现有契约。

无凭据/预算授权仍不阻塞离线工作。2D 的最小调用矩阵、额外 Schema 探针和 A-4/ES设施分离保持前轮要求，本轮不执行。Stage 1 不重开，整个 Stage 2 不因本轮方案复核而宣布完成。

审查树跟踪文件保持目标内容，旧证据均保留。本轮没有运行任何业务测试或新增最小实现，以上为设计规则与源码/API 证据，不把未来实现当作已验证。报告完成后停止。
