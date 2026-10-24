# Stage 2B 增量独立复核：64aac441

**结论：存在阻塞，不能验收 Stage 2B。** 已确认多项原修复有效，providers race 已独立通过；但干净提交仍不能整体编译，流提前关闭、累计 owner 和初始化发布仍有实际缺陷。

日期：2026-10-20。基线 **745c4433b4a99542c441b7a3d6fe958f88688caa**；目标 **64aac44155414403d5d4e007863987e1f79acfe2**。对象存在、基线为祖先，检查完整36文件增量及相关生产链，没有重审无关Stage1历史。

## 工作区与证据保护

复用 C:/Zhy/code/go-rag-review-1d3 的 detached linked worktree，common dir C:/Zhy/code/go-rag/.git。原仓库分支未切换/清理，未clone/fetch/改配置。未发现适用AGENTS.md。九份既有复现切换前保存至 preserved/ 并留哈希；历史报告未覆盖。

先在干净目标执行正式命令，再加入独立测试。最终生产文件 git diff --exit-code -- server 为0；十份review测试为未跟踪文件，副本/hash/diff存repros/，状态存final-worktree-status.txt。

证据分层：

- **提交内实测**：ordinary/race-providers/race-wiring/race-key-stress/full/build/vet.log，运行时未加入review测试。
- **目标＋独立复现，无overlay**：target-plus-review.log；race-independent-repros.log，生产字节完全来自目标。
- **目标＋未提交Config快照＋复现**：overlay-logic.log。仅用原仓库config.go只读快照通过Go overlay补齐字段以运行初始化/Reset复现，不改目标源码；supplement-sources/、overlay.json及original-uncommitted.diff记录来源/hash。没有复制未提交indexer.go。
- **实现者日志**：verification/stage2b-fix3/；其全量/build/vet通过不能由本提交对象复现。

## 阻塞问题

### 1. P1：交付完整性仍未闭合，Config.Registry及Index接线遗漏

位置：server/core/rag.go:89–90；server/internal/logic/rag/retriever.go:105；server/core/config/config.go:12（Config定义）；server/core/indexer.go:79（Index入口）。

目标core.New引用conf.Registry，但提交的Config没有此字段。实际编译报 `conf.Registry undefined`；默认工厂也传Registry字段。因而build、vet、全量及相关logic包验证无法完成。

只读原仓库差异确认config.go有真实未提交+5行（import和Registry字段），indexer.go有未提交+22行（任务预算注入/移交），不是CRLF。目标Index中没有WithTaskBudget/TransferToBackground调用，交付摘要“Index后台接线完成”不成立。

影响：干净对象不能构建；必要生产装配未交付。合并为一个交付完整性问题，勿逐个遗漏重复列阻塞。

最小方向：选择性纳入完整且正确的生产/测试依赖，在干净新提交对象验证；不要直接把原工作区全部add。未提交Index实现本身还创建新RequestBudget并丢弃移交句柄，不应不经检查直接补交，见问题3。

证据：ordinary.log/build.log/vet.log；original-uncommitted.diff。上一轮缺失ctx/transport/profileChat本次已提交并解决。

### 2. P1：提前Close无法打断阻塞的源Recv，slot/连接/goroutine仍被占用

位置：server/core/providers/bailian/chat.go:261–285；server/core/providers/budget_adapter.go:380–407。

两种包装都返回独立pipe reader，转发goroutine先阻塞inner.Recv，只有之后writer.Send发现下游关闭才调用源Close。消费者关闭返回pipe不会直接关闭HTTP源；当服务已发送首chunk后不再返回数据时，转发者无法走到Send。未设置独立取消桥接或可等待完成链。

**不使用overlay的真实HTTP复现** TestSupplementStreamEarlyCloseFinalizes：首chunk后服务保持连接，调用消费者Close后 InFlight=1、InputReserved=2、OutputReserved=8192；只有外部取消/释放服务才清理。test cleanup明确取消并等待handler和slot收敛。普通测试失败；-race-count10同条件10轮全部失败，非DATA RACE。

影响：用户停止接收后连接和预算并发槽继续被占用，后续请求可能被容量门禁拒绝；不能宣称“Close→源关闭→lease→goroutine收敛”。

最小方向：让下游Close/取消能直接终结底层源/请求与lease，所有后台消费者有可收敛所有权，不以“源以后继续发一条”作为唤醒前提。保留EOF/错误/提前Close的幂等、保守usage及Stage1 SSE契约；同时覆盖通用OpenAI和百炼包装。

测试真实性：提交TestStream_BlockedSourceClose在下游Close后**主动srcWriter.Close()**，这一步正是额外释放源，不能证明消费者Close驱动关闭。CancelNotEOF手工发送context.Canceled可以验证Abort映射，不能代替真实请求ctx取消链。TestStream_ConcurrentClose直接测试内部lsw，不等同返回reader并发关闭。

原三条消息截断问题已修复，TestIndependentStreamPreservesChunks通过；不能因修复Send判断就宣布整个流生命周期闭合。

### 3. P1：累计owner仍被刷新；TaskBudget与真实调用账本不相同

位置：server/core/common/budget_ctx.go:16–26、32–56；server/core/retriever.go:64；server/internal/logic/chat/chat.go:197、281。

**请求owner**：WithRequestBudget不检查已有owner，每次都NewRequestBudget覆盖ctx。Retrieve与GetAnswer/GetAnswerStream分别注入，尚无覆盖Retrieve→Rewrite/Rerank→最终生成的同一请求owner；嵌套入口会重置额度和固定deadline。创建失败还静默返回原ctx，使下游fallback，不能当作严格入口门禁。

**任务owner**：WithTaskBudget创建tb，然后另外NewRequestBudget注入ctx，两者是独立scope。实际适配器只消费rb，移交tb得到空账本，不能延续同步额度。目标Index尚未接此helper，见问题1；即使补交原工作区Index代码，其后台再次NewRequestBudget而非实际消费移交owner，问题仍在。

不使用overlay的共享生产helper复现：

- TestIndependentNestedEntryKeepsOwner：初始owner上限1，消耗后调用下一入口，owner指针变化，下一次预留成功，本应拒绝。
- TestIndependentTaskUsesTransferredScope：同步通过注入rb结算一次后移交tb，后台CommittedAttempts仍为0，仍可预留，本应继承耗尽状态。

两项普通失败，-race-count10各10轮全部失败。测试调用真实Registry/common helper和预算核心，无复制实现，无fake账本。

影响：子调用/入口切换和后台移交刷新累计限额，进程总额尚有余量时可绕过请求/任务预算。race通过不排除逻辑账本错误。

最小方向：最外层创建owner，嵌套入口复用；适配器消费的scope必须就是被移交的任务scope。错误显式传播，固定deadline和后台服务context按既定契约协调，不重新派生空累计owner。覆盖真实ChatStream编排和真实Index同步/后台路径。

测试真实性：提交TestOwner_ChatStreamShared手工注入rb并连续调用模型，不经过生产入口helper；TestOwner_IndexBackgroundTransfer直接操作TaskBudget核心，不经过Index或WithTaskBudget。这些局部测试有价值，但不足以证明摘要中的生产装配。

### 4. P1：core.New提前发布全局Registry且失败不回滚，生命周期回归

位置：server/core/rag.go:87–94；server/internal/logic/rag/retriever.go:106–114。

core.New在检查索引、构建依赖之前直接common.SetRegistry(conf.Registry)，没有保存旧引用或defer回滚。后续IndexExists等失败直接return，工厂仅返回error，所谓“成功再发布”被core.New中的提前发布破坏。

在**仅补齐未提交Config字段的overlay层**复跑原TestReviewRegistryPublicationOnInitFailure：本机httptest模拟索引初始化401，core.New失败后全局Registry仍被替换，测试失败。复现来源与Hash可追溯，不计作干净目标能编译。源码本身已能证明该return路径无回滚。

影响：失败初始化留下半成品路由，旧实例或并发请求可使用新模型/凭据/预算。此为此前已修复问题的回归。

最小方向：初始化通过显式实例级构造依赖使用本次Registry，成功后由拥有者原子发布，失败保持原实例精确引用；不要用无锁临时全局切换模拟局部依赖，简单回滚也必须防并发覆盖新代实例。

Reset超时Registry保留及身份核对仍保留，原复现通过，不要求重新改其正确部分。

## 原七项修复核对

| 原问题 | 结果 |
|---|---|
| B1提交依赖 | 旧API/helper已补，新Config字段和Index接线遗漏；未闭合 |
| B2流语义 | Send判断/多chunk完整性闭合；提前关闭阻塞源未闭合 |
| B3输出预留同源 | opts解析已接，原WithMaxTokens(16384)超8192复现现在发送前被拒，通过 |
| B4 OpenAI预算 | Generate/Stream共享预算层已接；原MaxRequests=1两次发送复现现在通过，正式OpenAI race通过 |
| B5 owner生产链 | processBudget跨Registry复用源码存在；入口/Task scope仍错误，并引入初始化提前发布回归 |
| B6重定向 | 默认及注入Client均禁止；本机307原复现及正式race通过，无第二次发送 |
| B7 Embedding usage | 改为per-call collector，串账修复源码合理；正式不同usage双请求测试及race通过 |

保留：Embedding新profile生产fail-closed、原始index乱序/重复/缺失验证、显式Chat cfg、Rerank错误传播、nil Transport/nil文档原复现、builder同步冻结、无递归委派。未发现因这些已修复项需要另开修复轮次。

## 实际装配与预算边界

ProviderConfig→buildRegistry（持久processBudget）→Registry→各角色builder→百炼独占预算或通用budget*Role→锁定SDK/旧Rerank HTTP。百炼Embedding/Rerank把内层Budget置nil，避免双重预留。真实OpenAI注册由生产import触发，预算现在生效。统一Transport禁止重定向包括注入Client。

Chat/Rewrite/QA默认通过Registry；显式Chat配置保持其既有兼容路径。Retrieve和Chat各自调用入口helper，但目前创建独立request scope，Index在提交内未迁移。core.New尝试通过全局提前发布取得本次Registry，存在问题4。故不能描述为“整请求/任务累计与完整初始化生命周期已保护”。

## 独立实测

实际CWD C:/Zhy/code/go-rag-review-1d3/server。Go1.27.1/windows/amd64；GCC16.2.0 MSYS2 UCRT64、binutils2.47.20260726；CGO=1，GOFLAGS空。仅进程PATH/CC/CXX/CGO设置；GOPROXY/GOSUMDB=off，无系统修改/升级，无真实模型/DB/ES/Qdrant调用。允许的HTTP均为本机httptest。

| 干净目标命令 | EXIT | 解释 |
|---|---:|---|
| 受影响包普通测试 | 1 | providers等可编译范围通过；core/logic-rag被Registry字段阻断 |
| go test -race ./core/providers/... -count=1 -timeout=180s -v | 0 | 三个providers包实际执行通过，无DATA RACE |
| common/rerank/logic-rag/chat race | 1 | 部分包通过，logic-rag因编译失败不能整体通过 |
| providers关键准确正则 -race -count=10 -v | 0 | 370顶层PASS，37个包限定用例×10；同名RetryNoRefresh分属两个包，共36个不同裸名称 |
| go test -p 1 ./... -count=1 -timeout=180s | 1 | 编译失败，部分独立包通过不算整体 |
| go vet ./... | 1 | Config.Registry未定义 |
| go build ./... | 1 | Config.Registry未定义 |

完整命令/正则/SHA/CWD/EXIT见run-clean.ps1及日志。本轮确实修复race运行环境，不再引用旧0xc0000139；也不把providers race扩大为所有包通过。十轮范围含模型预算、流、注册、重定向、usage；不含无法编译的logic-rag TestOwner_ProcessBudgetNotRefresh，不能把370PASS称为完整生产装配验收。

**目标＋review，无overlay**：target-plus-review.log，17顶层，14通过/3失败，另含3个index子用例。race-independent-repros.log准确选择关闭、嵌套owner、task scope三个用例，30RUN/30FAIL、DATA RACE=0、EXIT1，失败为真实断言。

**目标＋Config快照＋review**：overlay-logic.log，5顶层，4通过/初始化失败不发布用例失败。其余逻辑测试不自动算入正式目标。未使用原工作区未提交Index实现。

首次新增owner测试遗漏必须的CredentialResolver，只触发fixture构造失败；补正为只返回本地字符串的resolver后重跑。supplement-first-fixture.log和初始supplement.log保留，不把这些fixture失败当生产证据，也不重复计数。最终以target-plus-review.log/overlay-logic.log为准。

## 后续门禁和非阻塞边界

2C继续负责六处维度/模型身份fingerprint及写入、检索前guard；新Embedding profile生产启用仍被阻断。2D在线探针单独授权；A-4、真实向量库/ES启动、SQL driver取消及Stage1历史范围不扩大。本轮协议未改变，官方依据沿用independent-2b-a24cb8d/SOURCE_EVIDENCE.md；新增语义核对以锁定Eino源码为准，不声称在线行为验证。

格式、命名、归档样式不构成阻塞。建议复杂修复交给Qoder专家模式，提示词见QODER_PROMPT.md。停止等待新SHA，不更新验收状态，不宣布整个Stage2完成。
