# Stage 2B 修复独立复核：745c443

**结论：存在阻塞，不能验收 Stage 2B。** 干净提交仍不能编译；补充验证发现流被截断及预算绕过。未修改生产代码、提交、推送或更新验收状态。

日期：2026-10-20。基线 be28e918f4962e88906919e6f45ce39707cb9a50；目标 **745c4433b4a99542c441b7a3d6fe958f88688caa**。两对象存在且基线为祖先，审查累计27文件差异，并沿先前阻塞相关调用链核实。

复用 C:/Zhy/code/go-rag-review-1d3（detached linked worktree），common dir C:/Zhy/code/go-rag/.git；未clone/fetch，未切换主工作区。未发现适用 AGENTS.md。切换前保存原七个复现及哈希到 preserved/。最终目标生产文件 git diff 为空，九份独立测试留作未跟踪文件，副本/hash/diff 在 repros/。历史证据未覆盖。

## 证据层

1. **干净提交独立实测**：先在未加入任何复现时运行完整规定命令。日志 run-clean.ps1、environment.log、ordinary/race-providers/race-wiring/race-key-stress/full/build/vet.log。
2. **目标＋未提交依赖快照＋独立测试**：原仓库 budget.go、transport.go 存在真实未提交新增内容（不是行尾）。只读复制到 supplement-sources/，用 overlay.json 编译映射，不覆盖目标文件；未复制 registry_test.go 的 fake TestMain。所有补测均标此层，不算干净目标验收。
3. **实现者日志**：verification/stage2b-fix2/ 的 PASS 无法由目标提交树复现。

## 阻塞（位置均为目标源码）

### B1 / P1：仍遗漏提交依赖，干净提交不能编译

位置：server/core/providers/bailian/chat.go:139、embedding.go:155、rerank.go:86；providers/builder_sync_test.go:218、227；bailian/integration_test.go:173、566。

适配器调用未定义 RequestBudgetFromContext；新增测试还调用未定义 ContextWithRequestBudget、ErrRedirectProhibited；profileChat 仍未提交。原先 BuildConfig/FakeRegistry/ProviderConfig 遗漏已补，但交付完整性仍未闭合。

后果：build/vet/full/目标包race均无法整体完成；关键十轮不是通过。原仓库未提交 budget.go +22、transport.go +9、registry_test.go +45 可解释日志与提交对象差异，不能归因于 CRLF。

方向：选择性补齐真实生产依赖及必要测试；在干净新对象重验。不要仅添加 fake TestMain 掩盖真实构造路径。

### B2 / P1：Send 返回语义反用，正常流只输出第一条

位置：server/core/providers/bailian/chat.go:243–267，尤其260。

锁定 Eino v0.6.0 schema/stream.go:116–124 明确：Send 返回 closed，正常发送返回 false。当前 `if !writer.Send(...)` 把正常发送当成关闭，立即 lsw.Close 并 return。

独立补测 TestIndependentStreamPreservesChunks：经过真实 wrapLeaseStreamReader，输入三条消息，输出只有一条然后 EOF。见 independent-extra.log。用户会收到静默截断答案，后续usage/错误也丢失。

方向：按真实API修正判断；同时完成返回 reader.Close 到源取消/终结的所有权，不应只翻转条件后保留阻塞源 Recv 无法因下游关闭唤醒的问题。当前仍有 goroutine（250行），交付“无goroutine”表述不实。显式提前Close应保守终结，不能用部分usage按正常EOF退款。

证据限制：旧提前Close补测本轮通过，实因流已在第一条后自行关闭，**不证明生命周期修复**。目标 TestStream_CancelFinalizes 实际读取数组到EOF，没有触发context取消。应加多chunk完整性、真实阻塞网络源提前关闭/取消、终止错误及关闭并发验证。

### B3 / P1：固定8192预留仍可被真实输出选项绕过

位置：bailian/chat.go:72–83、105–114、154–170。

估算固定8192，Generate/Stream透传 model.Option，未从 opts 解析实际 MaxTokens，也未约束请求。修改默认数字没有形成同源契约，更没有从 Capabilities 读取上限。

TestSupplementOutputOptionAboveReservation：进程输出预算8192，传 WithMaxTokens(16384)，本机HTTP实际收到16384；不是预算耗尽拒绝。见 supplement.log。返回usage较小并不能证明服务最大输出受限。

方向：有效请求输出参数与预留同源计算，覆盖opts/默认值，超限发送前拒绝或明确限制；不可仅测试estimate等于常量。

### B4 / P1：新增 OpenAI builder 绕过全部预算

位置：providers/openai/register.go:27–40；providers/adapter.go:56–92（及Embed/Rerank共享构造）。

真实builder已注册，但直接调用 providers.BuildChatRole/BuildEmbedRole/BuildRerankRole；共享基础适配器没有Reserve/Commit/Finish，BuildConfig.Budget被忽略。

TestIndependentOpenAIBudget：真实 Registry→OpenAI builder→HTTP，MaxRequests=1，两次Generate均发送，账本AttemptsUsed=0。见 independent-extra.log。不同Provider配置可绕过整套预算，不能把仅百炼有预算说成全生产链覆盖。

方向：各已支持Provider经过共享预算执行路径，保留协议适配隔离；不能用fake builder替代真实预算HTTP回归。

### B5 / P1：累计owner与构造上下文尚未生产接线

位置：bailian/chat.go:133–150、embedding.go:150–168、rerank.go:80–98；internal/logic/rag/retriever.go:63–109、136。

即使补入ctx helper，仓库生产入口仍未调用 ContextWithRequestBudget；NewRequestBudget仅在适配器fallback及Registry包装中出现。没有ChatStream各阶段共享owner，没有Index task owner/后台显式移交。buildRegistry仍每次new ProcessBudget，不能通过重建Registry保持既定进程累计额度。

默认工厂仍使用 context.Background 构造局部reg，core.New未收到本次reg，之后才SetRegistry。core/rag.go:106、112、129仍通过common全局获取模型；无法保证初始化QA使用新配置。此处是源码证明的装配缺口，未声称调用未使用cm发出了旧模型HTTP请求。

方向：请求/任务入口创建并传递累计owner，后台显式移交且保留既定deadline语义；进程持有共享预算。新实例构造显式传入本次Registry或实例级构造依赖，成功才发布全局。不要仅新增context读取函数便宣布入口已接线。

### B6 / P1：重定向修复未提交，且注入Client仍可绕过

位置：providers/transport.go:78–108；bailian/chat.go:83及实际Client调用。

提交Transport未设置CheckRedirect；所谓RoundTripper计attempts在提交树中也不存在。原仓库未提交版本只给默认新建Client加禁止策略，WithHTTPClient路径仍沿用注入Client。

TestSupplementRedirectCountsHTTPAttempts 在overlay包含这份未提交transport时仍失败：注入合法本机srv.Client，MaxRequests=1，收到HTTP307后又发送一次，sends=2，AttemptsUsed=1。见 supplement.log。

方向：把发送/重发策略落实到共享生产路径，含注入Client；明确禁止或逐次计费，保留合法客户端策略，不能只测默认Client。原提交测试的重定向目标为外部域名，本轮因编译失败未执行；修复回归应全部使用本机httptest并断言实际发送数。

### B7 / P1：Embedding usage 通过角色共享字段回传，跨请求串账

位置：bailian/embedding.go:44–45、99–101、127–129、274–277。

lastUsage属于缓存角色实例，并发调用开始会清空，transport写入后调用再读取；每一步加Mutex只避免内存race，不保证请求关联。例如A写usage=100，B开始清空或写1，A再读取便将unknown/1结算给A。影响请求/任务与进程账本准确性，race检测通过也不能排除。

证据：源码完整读写交错证明，本轮未额外伪称并发HTTP复现。单请求 TestSupplementEmbeddingSettlesKnownUsage 已通过，只能证明串行路径修复。

方向：usage必须随单次HTTP/lease或调用上下文回传，不能放全角色lastUsage。加确定交错的双请求测试，usage不同，分别核对两个owner及进程账本；不能把整个网络调用加全局锁当默认解决。

## 原修复闭合情况

| 项目 | 本轮结论 |
|---|---|
| 遗漏依赖 | 旧三处已补，新ctx/transport/helper遗漏，未闭合 |
| 预算 | 百炼部分有真实接入；输出/重定向/OpenAI/owner仍阻塞 |
| Stream | Copy已删除，但新增第一chunk截断回归；未闭合 |
| Reset | 超时保留Registry独立补测通过，成功后清理有身份校验；初始化本次Registry装配仍未完成 |
| 显式配置/Rerank错误 | Chat显式cfg和错误传播补测通过；真实doRerank已不再吞构造错误回退 |
| OpenAI | 注册/离线构造通过，但预算绕过，新问题见B4 |
| nil Transport / nil doc | 原复现通过；nil doc在底层返回错误，不再panic。不能据此说所有本地验证均在Reserve前完成 |
| Embedding guard/index | 显式配置fail closed；原始乱序/重复/缺失拒绝补测通过，保留2C门禁 |
| Builder同步 | 原锁/冻结实现保留；正式race因编译失败不能验收 |

QA显式cfg仍委派既有qaDefaultCache（ready优先），本轮不重新扩大Stage1缓存审查，但不要把Chat的直接构造修复概括为所有角色已具备完整配置隔离。

## 实测与真实性

CWD固定 C:/Zhy/code/go-rag-review-1d3/server。Go1.27.1/windows/amd64，GCC16.2.0（MSYS2 UCRT64）、binutils2.47.20260726、CGO=1、GOFLAGS空；仅进程环境，GOPROXY/GOSUMDB=off。没有修改工具链或系统设置。

干净目标：受影响包普通测试、providers/... race、common/rerank/logic/rag/chat race、关键准确正则-race-count10、串行全量、build、vet，**各整体EXIT=1**。完整命令/正则/SHA/CWD/退出码见 run-clean.ps1 和每份日志。部分可独立编译的common/rerank等包执行成功不代表整体通过；providers关键用例因编译失败没有实际RUN。不是旧0xc0000139环境异常。

补充层：supplement.log 18顶层，16通过、2失败，另有index三个子测试；independent-extra.log 两个新顶层均失败（流截断/OpenAI预算）。没有把重复日志计数或当正式race。未对补充层重复跑race以替代提交内证据。

新测试来源：前七份复用preserved（保留原来源）；review_fix_behaviors调整输出预算8192/选项16384，并显式导入生产OpenAI注册包；新增review_blocked_stream_test.go（最终为消息完整性测试，名称保留来源）与review_openai_budget_test.go。全部源码/hash/diff保存在repros/。首次新增fixture出现重复Close清理panic，后续阻塞事件测试因第一条后自动退出不适用，两份日志保留为independent-extra-fixture-error.log及independent-extra-blocked-inapplicable.log，**不作为生产阻塞证据**；最终三条输入/一条输出验证替代该尝试。

目标新增 TestBudget_OutputCapMatches 只比较estimate与常量，不检查HTTP max_tokens；TestBudget_OwnerReuseViaCtx手工注入owner，不能证明生产入口已接线；流取消测试以EOF冒充取消；这些不足集中由上述真实回归覆盖，不因测试数量额外重复列问题。

## 保留边界与结论

协议及模型未变，沿用上轮官方资料记录 independent-2b-a24cb8d/SOURCE_EVIDENCE.md；本轮新增依据为锁定Eino本地源码，未凭经验猜API，也未执行真实服务。qwen3-rerank名称/平面兼容协议本身不是阻塞。

2C负责维度六处、fingerprint及真实写入/检索guard；新Embedding生产门禁继续保留。2D在线调用须单独授权。A-4、真实ES/Qdrant、SQL取消及Stage1历史补证不扩大本轮。

无命名/格式类阻塞。不更新验收状态，不宣布Stage2完成。建议Qoder专家模式集中修复，见QODER_PROMPT.md；停止等待新SHA。
