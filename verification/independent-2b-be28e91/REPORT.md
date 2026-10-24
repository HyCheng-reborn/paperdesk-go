# Stage 2B 修复独立复核

结论：**存在阻塞，不能验收 2B。** 干净目标本身无法编译；隔离补测另发现预算、流生命周期、Reset 和路由缺陷。此次没有修改生产代码、提交、推送或更新验收状态。

- 日期：2026-10-19。
- 基线：a24cb8d609e6c4580b0726cb7de0ccd21d2efdd1。
- 目标：be28e918f4962e88906919e6f45ce39707cb9a50。
- 两对象存在，基线为目标祖先；审查整个增量，结合原 2B 问题验证。
- 复用 detached linked worktree：C:/Zhy/code/go-rag-review-1d3；common dir 为 C:/Zhy/code/go-rag/.git。未 clone/fetch，未切换或清理原仓库。
- 切换前将原五个独立测试移动至本目录 preserved/ 并保存哈希；历史报告未覆盖。当前七个独立测试为未跟踪文件，源文件及 diff/hash 留在 repros/、repro-hashes.txt。目标生产文件 git diff 为空。

## 证据分层

**A：干净目标。** 在添加本轮测试前完成全部规定命令，详见 run-clean.ps1、environment.log 和各日志。所有规定命令整体 EXIT=1，首先失败于编译；不是 Windows race 启动异常。

**B：目标＋未提交依赖快照＋独立复现。** 为避免只补交文件后再次暴露行为问题，只读保存原仓库 adapter.go、fake.go、config.go 三份未提交文件，用 Go -overlay 映射编译；没有覆盖目标文件。overlay.json、supplement-sources/ 保存映射、原始 diff 和哈希。没有引入原仓库未提交 registry_test.go 的 fake TestMain/profileChat。此层不构成目标提交自带测试通过，也不闭合正式 race 门禁。

**C：实现者日志。** verification/stage2b-fix/ 中 PASS 声明无法在提交对象重现，不能作为本目标验收证据。

## 阻塞问题（行号均为目标提交生产文件）

### 1. P1：交付遗漏依赖，干净提交不能编译

位置：server/core/providers/registry.go:232、266、302；adapter.go:29；fake.go:247；server/core/config/config.go:35；server/internal/logic/rag/retriever.go:134；providers/builder_sync_test.go:198、207。

Registry 构造 BuildConfig{Budget:...}，但提交的 BuildConfig 无 Budget；FakeRegistry 不实现新增 NewRequestBudget。编译器直接报这两类错误。继续核对还发现 ProviderConfig 缺四个新预算字段，builder 测试引用未提交的 profileChat。原仓库相应文件存在真实未提交内容，不能归因于 CRLF。

影响：build、vet、全量及目标包普通/race 测试均不能完成。必须合并为一项交付完整性问题，而非把每个遗漏重复列为独立阻塞。

最小方向：选择性补齐实际生产定义和必要测试依赖，在干净提交树重新执行。不能仅通过安装 fake builder 的 TestMain 掩盖真实构造缺失。

证据：build.log、ordinary.log、race-providers.log、vet.log；supplement-sources/ 原仓库快照。

### 2. P1：预算仍未覆盖真实发送边界和累计 owner

位置：providers/bailian/chat.go:66–144；embedding.go:100；rerank.go:46–77；internal/logic/rag/retriever.go:114–134。

三个适配器每次调用自行 NewRequestBudget，未复用请求/任务入口持有的 owner；后台移交也未接上。buildRegistry 每次创建 ProcessBudget，尚无明确的进程共享持有者。当前只对单次逻辑调用外包 Reserve/Commit，不能描述为完整请求/任务累计预算。

隔离补测的实际结果：

- TestSupplementRedirectCountsHTTPAttempts：MaxRequests=1，HTTP 307 导致本机服务收到两次发送，账本只记录一次。默认客户端重定向绕过实际尝试上限。
- TestSupplementOutputCapMatchesReservation：只预留 4096，实际请求 max_tokens=8192，返回 usage=4097，OutputUsed 超过 4096 限额。预留值与真实输出上限不一致。
- TestSupplementEmbeddingSettlesKnownUsage：原始响应 usage=100 被忽略，账本仅记录估算值 1。并非服务没有 usage。
- TestSupplementInvalidInputDoesNotSpendAttempt：topK=0 本地校验失败、零 HTTP 发送，却消耗一次 committed attempt 和输入额度。Commit 早于真正可发送边界。

最小方向：入口持有累计 owner，并显式传递/移交；进程预算在既定进程作用域共享。实际发送前按协议/客户端重试与重定向规则预留计数，禁止绕过或明确禁用多余发送；先完成本地验证，真实 max_tokens 与预留一致；保留并结算已知合法 Embedding usage。不要以普通核心 Reserve 单测替代真实 HTTP 链验收。

### 3. P1：流式 Copy(2) 引入第二消费者，提前 Close 不终结底层流

位置：providers/bailian/chat.go:163–185。

包装创建两支 StreamReader，另起 goroutine 消费 usage。锁定 Eino 的 parentStreamReader 仅在所有分支关闭时关闭源；调用方关闭第一支时，第二支仍可能阻塞读取，持有连接和 lease，还改变背压/缓存行为。

TestSupplementStreamEarlyCloseFinalizes：读到首条后消费者 Close，服务保持连接；InFlight 仍为 1、OutputReserved 仍为 4096。仅外部取消/释放服务才收敛。测试清理显式取消并等待资源退出，不把 goroutine 提前信号当完成。

最小方向：用单一拥有者包装真实 Recv/Close，在 EOF、终止性错误、取消或提前 Close 上关闭源并幂等终结。不要添加无人管理的第二消费者；失败与正常 usage 结算须区分，不能因部分 usage 给中断流不当退款。

### 4. P1：Reset 等待前清 Registry，Shutdown 超时仍破坏原实例依赖

位置：internal/logic/rag/retriever.go:590–594。

Reset 首先 SetRegistry(nil)，随后才等待 Shutdown。即使失败保留 Rag、向量库和初始化状态，Registry 已丢失；尚在运行的工作可转入旧模型/旧配置路径，违反 Stage 1 超时不拆依赖契约。

TestSupplementResetTimeoutPreservesRegistry 经过真实 Rag.Shutdown 和真实 WaitGroup/done：返回 deadline exceeded 后 GetRegistry()==nil。测试完成后释放任务并等待生产 done。

此外 defaultRagFactory:70–106 虽延后发布 Registry，但尚未把本次局部 Registry 提供给 core.New 的构造上下文；初始化依赖仍查全局。该点为源码装配缺口，不声称已复现未使用 cm 的真实旧模型 HTTP 调用。

最小方向：完整等待成功后，在实例身份/初始化代次确认与既有锁规则下清理 Registry；失败保留原引用。新实例构造显式使用本次 Registry，成功再原子发布，失败不污染全局。

### 5. P1：配置选择和 Rerank 错误路由仍不一致

位置：core/common/chat_model.go:49、96、228；core/retriever.go:276–282。

GetChatModel 等在检查显式 cfg 前无条件选择全局 Registry，非 nil 显式模型配置被忽略。TestSupplementExplicitChatConfigNotDiscarded 证明指定配置却返回全局 fake 角色。修复默认旧 singleton 优先级，不应扩大为覆盖所有显式配置。

真实 doRerank 在 GetReranker 返回任何错误时仍继续旧配置/旧 Rerank，可能在新 Provider 构造失败时静默换模型/预算；此项由源码路径证明，未伪称已做 HTTP 复现。

最小方向：明确 cfg==nil 默认路径与显式 cfg 的规则；不支持的组合返回可识别错误。已启用 Registry 角色的构造失败必须传播，只有契约允许的“未配置”才兼容旧路径。

### 6. P2：声明支持的 OpenAI 无真实 builder

位置：providers/registry.go:232–239（及 Embed/Rerank 对应路径）；providers/bailian/register.go:10。

移除旧默认构造后，生产只发现 bailian 注册；openai 并无实际 builder。TestSupplementSupportedOpenAIBuilder 在正常生产注册环境返回 ErrUnknownProvider，known:[bailian]。

builder_sync_test.go 中安装的 fake openai/bailian 构造器及原仓库未提交 TestMain 只能证明 fixture，不能证明生产 OpenAI 构造链。

最小方向：保留/注册支持的真实构造器并验证生产导入必然注册、无外部初始化；保持 map 同步、冻结及重复/nil 检查。无需因 init 本身增加重构。

### 7. P2：新包装层引入合法输入/客户端 panic

位置：bailian/embedding.go:180–190；rerank.go:46、85–94。

- 验证 transport 直接调用注入 Client.Transport.RoundTrip；合法的 http.Client{Timeout:...} 中 Transport=nil 本应使用 http.DefaultTransport，现在 panic。重建 Client 还丢失 CheckRedirect/Jar 等注入策略。
- Rerank 新估算器在原验证前解引用 nil document，原可识别输入错误变为 panic。

TestSupplementEmbeddingDefaultRoundTripper、TestSupplementRerankNilDocPreservesError 均捕获真实 panic 并使测试失败，没有让生产 recover 吞掉断言。

最小方向：尊重标准 Client 的默认 Transport 和既有策略，包装/复制时明确所有权；先做共享输入校验再估算/预留，保留原错误链。原始响应 ReadAll 的大小边界也应与既有 HTTP 防护保持一致。

## 原 B1–B6 修复核对

| 原项 | 本轮结论 |
|---|---|
| B1 预算接线 | 部分接入；真实单次 HTTP 超预算拒绝补测通过，但上述发送/owner/usage/输出上限仍阻塞 |
| B2 Embedding 门禁 | 源码及补测确认显式新 Embedding 配置 fail closed，未配置保留旧路径；不要求提前完成 2C |
| B3 原始 index | 在 SDK 丢失 index 前验证，乱序/重复/缺失三个补测通过；新增 Client panic 另见问题7 |
| B4 路由 | 旧缓存不覆盖 Registry、Chat factory 传播错误已通过补测；显式 cfg/Rerank 仍有缺口 |
| B5 生命周期 | core.New 失败不发布新 Registry 补测通过；Reset 超时仍不保留完整依赖 |
| B6 同步 | 源码已有 RWMutex、冻结、nil/重复/未知检查；不能因干净树无法编译宣布 race 闭合，且真实 OpenAI builder 缺失 |

## 实际装配与覆盖

配置 ProviderConfig → logic/rag.buildRegistry → Registry（绑定 ProcessBudget）→ builder hook → 百炼 Chat/Embedding/Rerank adapter → 锁定 SDK/旧 Rerank HTTP 实现。Rerank 无循环委派补测通过。common 默认 Chat/Rewrite/QA 优先 Registry，Chat 工厂不再吞错误；旧实现仍用于未配置及当前错误回退分支。新 Embedding 生产配置由 guard 阻断，离线直接构造仍允许。

预算目前在百炼适配器逻辑调用层，尚不能宣称真实每次 HTTP 发送、请求子步骤和后台累计全覆盖；OpenAI 构造不可用。注册 init 为内存注册，未发现其启动外部设施。

## 独立验证

实际 CWD：C:/Zhy/code/go-rag-review-1d3/server。环境详见 environment.log：Go1.27.1/windows/amd64，MSYS2 UCRT64 GCC16.2.0，CGO=1，GOFLAGS 空；仅进程设置，无工具链安装/系统改动。GOPROXY/GOSUMDB=off。未连接真实设施或模型。

| 干净目标命令 | 退出码/解释 |
|---|---|
| 受影响包普通测试（精确参数见 ordinary.log） | 1，providers 编译失败；部分不依赖失败代码的包执行成功不能代表整体通过 |
| go test -race ./core/providers/... -count=1 -timeout=180s -v | 1，编译失败，无该范围实际测试执行 |
| common/rerank/logic/rag/chat race（race-wiring.log） | 1，受编译失败影响；部分 rerank 执行结果单独保留 |
| 关键准确正则 -race -count=10 -v | 1，编译失败，目标关键用例 RUN=0；不是十轮通过 |
| go test -p 1 ./... -count=1 -timeout=180s | 1，编译失败 |
| go vet ./... | 1，编译失败 |
| go build ./... | 1，编译失败 |

run-clean.ps1 保留完整命令和关键正则；每份日志记录 SHA、CWD、日期和 EXIT。没有用普通测试替代 race，也没有把 RUN=0 当通过。

补充层命令见 supplement-overlay-final.log：四个叶包执行 ^Test(Review|Supplement)，17 个顶层（8通过、9失败），另含 index 三个子用例。随后单独运行 TestSupplementEmbeddingSettlesKnownUsage，失败，见 supplement-known-usage.log；合计18个不同顶层用例，不把两次旧日志重复计数。补充层未执行 race，不替代干净目标验收。

复现路径：repros/ 中七个 review_*.go 源文件和 diff；实际位于对应 server 包中。旧 review_original_protocol_test.go 由 preserved/review_protocol_test.go 复制，仅把 attempt 隔离用例的输出限额100改为0，避免新4096预留遮蔽待测 attempt 行为；其余原测试按来源保留。overlay 的三份依赖是原工作区未提交内容，不算目标交付。

测试真实性：目标的 TestBudget_StreamFinish 仅检查新预算快照，未实际调用 Stream；部分 Retry/Usage 测试直接操作 lease，不能证明 SDK/HTTP 接线。缓存/注册 fixture 可作为局部单测，但验收还需要真实共享构造、body/stream 和发送路径。

## 后续边界与建议

2C 继续负责六处维度、索引 fingerprint、真实写入/检索前 guard；当前禁止新 Embedding profile 生产启用。2D 负责授权在线协议/usage/流行为探针，本轮未执行。A-4、真实 ES/Qdrant、SQL driver 取消及 Stage 1 历史 race 不因本轮扩大或宣称通过。

qwen3-rerank 的平面兼容协议与模型名称本身不是本轮阻塞。原官方资料核实保留在 independent-2b-a24cb8d/SOURCE_EVIDENCE.md；本轮协议未改变，主要用锁定依赖源码核对。Eino schema/stream.go parentStreamReader 的分支关闭规则支持问题3；OpenAI SDK embedding 映射丢失 index 的边界仍由前置验证覆盖。真实服务行为未验证。

非阻塞：证据归档命名、普通格式整理不要求新增修复轮次。建议复杂修复交给 Qoder 专家模式；具体提示词见 QODER_PROMPT.md。完成集中报告后停止。
