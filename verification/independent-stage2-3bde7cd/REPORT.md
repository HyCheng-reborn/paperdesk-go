**结论：修改后可开始首批。当前方案有三项阻塞 2A 的可实施性问题；另三项仅阻塞相关后续批次，不要求先完成在线验证。**

目标完整 SHA：3bde7cd881672d54b99aef0cd8d02a27cc635d6d。
基线完整 SHA：69984c4038d3bc04f822bdc6e31e40af73e3dc44，已核实为目标祖先；累计仅三份方案/状态文档改变，生产代码沿用已验收基线。本轮不重新审查 Stage 1 历史批次。

审查方式：读取 design/stage2-plan.md、总方案 docs/go-rag_PaperDesk_改进方案_v1.1.2.md §7.4、PROJECT_PROGRESS、CHANGES、go.mod/go.sum、实际生产装配和锁定依赖本地源码；核对官方百炼文档（2026-10-19）。未找到适用 AGENTS.md。未实现代码或修改方案，未运行全量测试、服务、数据库或模型 API；仅做两次离线 go doc API 核实，见 eino-api-check.log。

**阻塞首批 2A 的问题**

**A1／高：请求预算的退款与重试规则直接违反实际尝试上限，且 Reserve/Release 无法表达其承诺的多资源生命周期。**

章节：§4.1–4.5、§7.1 的 budget API；行 134–175、315–317。

证据与后果：§4.3 要求每次实际发送占请求数，§4.4 却把网络失败的请求数退回，§4.5 又仅以 MaxRequests 限重试。一次请求已到服务端、客户端在响应前超时，是无法从 net.Error 判断是否执行/计费的；退回后 MaxRequests=1 仍可再次发送。其他 token 额度即使暂时限制了次数，也不能修复请求尝试上限被绕过的事实。该规则无需在线调用即可判定不安全。

请求与进程两个作用域、请求数/输入/输出/并发多个资源如何原子整体预留没有确定；只有单计数器 Add(-1) 和可任意 Release(n)，缺少每次 attempt 的所有权及一次性结算。usage 缺失仅说按最大输入估算，没有说明已预留最大输出；流中断可能不知道实际消耗，却要求记录实际输入/输出；并发 slot 在响应头返回、body EOF、stream.Close 哪个时间释放也未规定。首批已经编写 budget.go，不能把这些语义留给各 Provider 自行猜测。

最小修正：区分总发送尝试计数（发送后单调消耗）、在途 slot（结束释放）、token 预留与账本；仅确定尚未发送的失败可撤销尝试预留。采用每 attempt lease/reservation，明确一次性 Finish/Abort/Close、双作用域全成或全退、overflow/非法上限/取消行为。未知 usage/发送后网络失败/流中断保守保留输入与输出预留，不把 unknown 当 0；实际 usage 超过估算时不能产生负债后仍继续放行。唯一重试执行者有有限尝试数、退避及绝对 deadline，已向用户输出的 stream 不自动重放。输出限制实际进入 SDK MaxTokens/HTTP body，不能只是预算字段。token 粗估仍不冒充精确金额或真实 token 硬上限。

锁定 SDK核查：OpenAI/Qwen 模型和 embedding 配置均支持 HTTPClient，设置后原 Timeout 不再生效；必须由实际请求 context 保证期限。锁定 go-openai v0.1.0 的 sendRequest/sendRequestStream 各调用 HTTPClient.Do，核对路径中未发现 SDK 自动重试循环，不应套用其他语言 SDK 的默认重试推断。方案仍需明确重定向、下层发送路径、可重放 body 及每次计数的观察点，避免一个外层预留包住多次实际发送。

**A2／中：首批接口不能按草案落地到锁定 Eino，缺少所需的桥接决定。**

章节：§3.4、§7.1，尤其行 291。

直接证据：Eino v0.6.0 components/model/interface.go:30–33 的 Stream 返回 *schema.StreamReader[*schema.Message]，model 包没有 StreamReader。离线 `go doc .../model.BaseChatModel` EXIT=0；`go doc .../model.StreamReader` EXIT=1。Generate/Stream 还有 opts ...model.Option；embedding.Embedder 的入口是 EmbedStrings(...,opts ...embedding.Option)，而草案只有 EmbedDocuments/EmbedQuery。

当前 Rag、Chat、Eino indexer/retriever 持有 model.BaseChatModel/embedding.Embedder。自有 Role 接口本身可以不同，但必须给出可编译的 Role↔Eino 适配，说明 output cap/model options、query/document 差异如何传递，不能一边称 Eino 类型不外露、一边让未定义返回类型直接替换生产字段。Extract 被要求独立角色，却没有 registry 获取入口；能力声明也只停留在文字。

最小修正：采用实际 generic schema reader，或定义明确 wrapper 并列出转换/Close/取消职责；选择嵌入 Eino interface 或显式 adapter，并以编译期 interface assertion 验收。固定 capabilities 的最小结构和 unsupported 错误；Extract 明确是同一 ChatRole 的角色标识/alias 还是独立获取入口，无需提前建立提取工作流框架。

**A3／高：首批范围、默认装配及缓存身份互相矛盾，缺少一条不会绕过新体系的生产链。**

章节：§3.1–3.3、§7.1/7.2/7.6、§9.1。

§3.3 称 2A 改 common 兼容层，但 2A 文件表全为新增，§7.6 又明确不修改既有代码；2B 仅列 bailian 子目录和 config，没有将默认创建/调用迁移到 Registry 的执行批次。仅测试 fake.GetChat 不能证明默认代码使用新的 budget/client。

实际漏列链：
- 最终答案并不只用 core/rag.go 的 x.cm：internal/logic/chat/chat.go:44–49 的 defaultChatModelFactory 直接 openai.NewChatModel；controller ChatStream 在 Retrieve 后另行 answerStream。仅修改 common.GetChatModel 或 Rag.New，最终生成仍可绕过预算和 transport。
- 同步 indexer、异步 indexer、内容/QA retriever 分别调用 common.NewEmbedding（indexer/indexer.go:19、indexer_async.go:18、retriever/retriever.go:25），当前不是 Rag.New 创建并共享同一个实例，方案 §5.2 的“已确认”不成立。
- Rewrite 默认在 core/retriever.go:76 取模型；Rerank 在 :261 用 NewRerank(...,nil) 直接走默认 client；QA 预热及 factory 还有 core/rag.go:106–112 的实际入口。
- 给 Registry 加 RWMutex 只能防竞态，不能防跨 profile 误复用。当前 GetChatModel 先返回包级 chatModel、不看 cfg；QA 是 mu+ready、失败可重试的 qaDefaultCache 包级默认实例，不是方案写的 sync.Once。迁移后若保留这些读取入口或只按 role 缓存，切换 endpoint/model/维度/凭据仍可能返回旧对象。

最小修正：列一张按批次的默认创建→注入→业务调用→HTTP 尝试映射，覆盖真实 Chat factory、所有 embedding 构造、Rewrite/QA/Rerank。明确 Registry 构造的 immutable Profile 和 owner：优先每配置实例独立持有；若共享缓存，键至少覆盖 provider/协议、endpoint/地域、模型/版本、维度、关键选项及凭据身份，凭据只用安全标识，不能写明文日志。进程预算由进程 owner 持有，不能通过新建 Registry 重置全局额度。兼容层收到非 nil 配置必须参与身份，不能仍由旧无键单例抢先返回。

2A 可以只先落实可复用的生产共享预算/适配核心，不必实现百炼，但不能只给空接口和 fake 自测；2B 必须承接默认工厂和业务接线。已改兼容层时，回滚不能仅删除 providers 目录，需一起 revert 引用它的提交。

**只阻塞后续批次的问题**

**B1／高，阻塞 2B Embedding 适配：复用 SDK 后再校验 index 已经太晚。**

章节：§5.1、§6.2、§7.2。

锁定 libs/acl/openai@v0.1.3/embedding.go:151–169 在 CreateEmbeddings 后按 resp.Data 顺序复制 Embedding，不读取 d.Index，并把非指针 Usage 数值转为 callback。上层 EmbedStrings 只剩 [][]float64，已经无法辨认乱序、重复/缺失 index，也无法仅凭全零 Usage 分辨“字段缺失”和“明确返回零”。

最小修正：在原始 JSON/raw SDK response 仍完整的边界验证 count/index 集合并重排或明确拒绝，校验 dimension/NaN/Inf，再交给 Eino；保留 usage presence。可复用底层 raw client 或独立小解析器，不要求升级依赖，也不要仅在 [][]float64 wrapper 声称 index 校验已实现。httptest 必须经默认共享构造及真实解析路径覆盖乱序、重复/缺失、错误数量、非法向量和缺失 usage。普通“fake 返回正确向量”不够。

官方同步 API 文档支持 text-embedding-v4 的批次/长度和可选维度；可确认 10 条、8192 token 与默认 1024，但支持维度为枚举值，不是任意 64–2048 连续整数。该限制可离线配置校验，账户可用性留探针。[官方同步 Embedding API](https://help.aliyun.com/zh/model-studio/text-embedding-synchronous-api)（查阅 2026-10-19）。

**B2／高，阻塞 2C 及任何切换 Embedding profile 的生产启用：维度迁移漏掉真正创建入口，fingerprint 尚无可实施的存取/旧索引规则。**

章节：§5.2–5.6、§7.3。

真实 Rag.New:87–92 调用 config.Config.IndexExists/CreateIndex，后者 core/config/helpers.go:40/45/57/61 也写死 1024；修改 vector/es.go 和 vector/qdrant.go 不会覆盖此入口。同步/异步 Qdrant indexer 的 VectorDim 在 indexer.go:73、indexer_async.go:52 仍为 1024。common/es.go 还有历史 helper，需确认是否仍可达，而非把所有位置混称两处。

当前 VectorStore 只有 IndexExists/CreateIndex 等接口，没有读维度/模型身份的接口；现有 New 对已存在索引只做 exists 检查。方案未指定 fingerprint 实际写在哪里、如何在 ES/Qdrant 读回，也未规定旧索引无 fingerprint 时怎么办。{model_id,dimensions} 不能区分不同 Provider/版本/语义配置下同名同维模型；维度相等不证明向量空间一致。

最小修正：确定统一 EmbeddingProfile 身份（Provider/模型版本/必要语义配置＋维度），指定两种后端都可落地的持久记录/读取接口及旧索引 fail-closed 或人工确认迁移流程；不要假定 Qdrant 任意元数据接口存在。Rag/所有 indexer/retriever 必须经同一个 guard/构造配置。在写向量或发送 kNN 之前确认身份和两个向量字段实际维度；不匹配不自动建、删、覆盖或重建用户索引。旧数据不可由代码 revert 撤销；回滚不能仅“恢复 1024”后继续访问新维度索引。

2B 可以先独立实现 httptest 适配器，但新 profile 的生产启用应在 2C guard 就绪后；这不要求先有真实 ES/Qdrant，离线可验证元数据请求/解析与调用顺序，真实后端往返另列设施验证。

**B3／中，阻塞 2B Rerank 生产接线：保留校验链与“不改 rerank.go”的要求没有连接方案，协议响应不能混合。**

章节：§3.2、§6.3/6.4、§7.2。

现有 rerank.go 的 Req 是扁平合并 Data/Conf，响应直接读取顶层 Results，HTTP、限长、解析、结果校验与独立副本输出在同一实现内。单独新建 bailian/rerank.go 并不自动继承这条校验链；而旧调用处仍 NewRerank(...,nil)，更不自动使用新适配器。需明确共享校验/输出 helper 或有边界的适配，不应为满足文件“不修改”而复制一套长期分叉的规则。

官方核查不把兼容路径判错：官方英文 Rerank 页明确示例 /compatible-api/v1/reranks 与扁平请求；中文原生 API 文档对 qwen3-rerank 同样支持原生嵌套 input/parameters，响应为 output.results。因此协议选择不能仅由“qwen3 模型=兼容/gte=原生”隐式推断。方案的通用 results[] 映射需要区分 envelope。[兼容与原生示例](https://help.aliyun.com/en/model-studio/rerank)，[原生响应定义](https://help.aliyun.com/zh/model-studio/text-rerank-api)（查阅 2026-10-19）。

最小修正：Profile 显式指定协议，encoder/decoder 分开；允许为复用定点抽取原有纯校验/输出逻辑并保留旧测试经过共享实现。默认 Rerank client 必须接入预算；兼容响应未由引用页面明确说明的细节可用最小探针冻结，不编造“已确认”。不要求现在调用在线服务。

**首批建议：缩小且可验证的 2A，而非等待全部在线未知项**

修订 A1–A3 的契约后，可开始以下准确范围：
1. 不可变 ProviderProfile/角色和最小 capabilities，使用锁定 Eino 的可编译 Stream/Embedding 适配；Extract 先作为角色标识或共享 Chat 配置，不提前实现论文抽取框架。
2. 实际会被 2B 使用的共享预算执行核心：request/task owner、process owner、attempt lease、deadline/并发管理、一次性结算；用 fake sender/httptest 驱动这同一执行核心。
3. 一个明确构造入口及现有模型→Role/Eino adapter 的生产共享链。若首批保留旧 Provider 协议，列出接入的具体工厂；其余工厂迁移放 2B 并写清，不能宣布全应用预算已生效。
4. default 构造只发布完整初始化对象。测试用实例依赖注入，不设可变包级函数替换；模型缓存 per Profile 隔离，失败不发布并可重试。

必要验收：编译期接口 assertions；并发争用无超额且双作用域预留失败完全回退；发送后网络失败不退款尝试额度；重试按实际次数计；取消停止发送并可识别；stream EOF/错误/Close 均一次释放在途槽并一次结算；缺失 usage 保留预留；不同配置/凭据身份不复用旧模型；默认工厂共享构造而非仅 fake 自测。定点普通/race 即可，不要求先完成 2B 协议或真实向量库。

Stage 1 生命周期约束需写入首批 owner 合同：Index 后台 context 来自服务 root，会丢掉请求 ctx 的 value（core/indexer.go:164），不能指望 ctx 携带的请求预算自动继承。显式把同一个 task budget/只读 owner 移交给后台，保留 HTTP 返回后继续执行和 Shutdown 等待语义，不把请求 cancel 重新接回后台。ChatStream 的 Retrieve 和最终生成共享一次会话预算，不为每个子步骤重新给满额。

stream 保持现有 reader/Close 所有权与 SSE clean EOF 才 DONE、不保存失败部分回答的契约。若需观察 usage/释放 lease，应透明观察实际消费或 transport body/stream 关闭，不新增与消费者抢读的 goroutine；租约不能在仅返回响应头/reader 时就释放，也不能拦截终结帧破坏 clean EOF。

**可以留到 2D 或设施验证的未知项**

- 已授权账户/地域实际可用模型、实际 endpoint/usage/结构化能力；官方支持不等同具体模型/账号探针已通过。JSON Schema/tool calling 必须按模型声明能力，不先假设可用。
- 真实 ES/Qdrant schema/fingerprint 往返、ES client/server 启动兼容。A-4 应单列 QA 缺向量设施探针，尽可能使用已知合成向量，不与付费模型连通性混算。
- 实际 Embedding 返回维度和 Rerank 兼容响应细节；官方 Embedding 默认值可作为候选，不当账号验证。

2D 执行前仍需补清：5 次最小矩阵如何含重试；每角色输入/输出/并发/绝对期限；总预算未知 usage 时的保守处理；Schema 能力额外调用是否列入授权。全局开关默认关闭，有凭据不等于预算授权。每次实际尝试消耗额度，任何非预期结果停止；Evidence 只含必要状态/usage presence/模型标识/耗时/脱敏请求 ID，不记录 key 或敏感原文。无授权不阻塞独立离线 2A–2C。

**官方核实的正向结论与非阻塞建议**

- 百炼 Chat 的兼容接口和 stream_options.include_usage 有官方示例；锁定 acl/openai 的流式请求也设置 IncludeUsage。HTTPClient、MaxTokens、ResponseFormat（含 JSON Schema 配置）在锁定 OpenAI/Qwen 组件中存在，但不证明每个模型均支持全部能力。[官方 Chat 文档](https://help.aliyun.com/zh/model-studio/compatibility-of-openai-with-dashscope)（2026-10-19）。
- 不新增依赖、区分兼容和原生协议、禁止静默换模型、将精确金额与估算区分、将无凭据标未执行，均可保留。
- gte-rerank 的文档退役日期 2026-05-30 已早于本轮日期，不宜作为新的在线探针默认候选；作为旧协议/历史兼容背景保留即可，不要求现在核验退役服务。
- §9.2 “2D 后方案阶段结束才实施”与前面各批代码验收顺序不一致，统一成修订方案→2A/2B/2C离线实施→授权2D即可；这是流程澄清，不单列阻塞。
- SDK 的缺失 usage 与零数值处理、配置泄漏、task budget 移交应进入真实共享路径测试，避免重复此前只测试 fake/helper 的验收问题；无需扩大成通用多 Provider 插件系统。

**证据、边界与保护**

锁定主要依赖由目标 go.mod/go.sum 核对：Go 1.23.1 模块要求，Eino 0.6.0，OpenAI model v0.1.6-0.20251202024135-b0f57b984d19、embedding v0.0.0-20251202024135-b0f57b984d19，Qwen v0.0.0-20250610035057-2c4e7c8488a5，acl/openai 0.1.3，底层 go-openai 0.1.0；GoFrame 2.9.0。实际只读 SDK 源文件位置见 SOURCE_EVIDENCE.md。

审查目录保持目标提交内容，旧独立测试保存于本目录 preserved/review_production_chain_test.go 和 hash.txt。无新的临时业务文件、无生产/方案改动，无全量测试或模型探针。go doc 的不存在符号 EXIT=1 是 API 证据，不是业务测试失败。没有将原工作区缓存远端引用当作实时远端状态。

Stage 1 关闭状态未重开；这里只检查 Stage 2 迁移必须保留的既有契约。本轮不要求所有后续问题和在线未知项在 2A 前解决。建议集中修订 A1–A3 后开始首批，B1–B3在对应批次前补齐；不逐条启动代码修复，不更新验收状态。报告完成后停止。
