# Stage 2A 独立源码复核与离线验证

**结论：存在阻塞，当前不能验收 2A。** 已有普通测试、build、vet 通过，但预算账本、owner 取消/期限及 Profile 缓存隔离存在可复现缺陷；必要 race 证据未完成。此结论不重开 Stage 1，不要求本批完成 2B/2C/2D。

## 固定目标、工作区及保护

- 日期：2026-10-19；完整目标：`5bf62f0069a527b4940b8336f3cb953ccb551813`。
- 基线：`212ccd8cc4c5743e6e34dfe5cb73e02b7a794b84`，已核实为目标祖先。审查完整累计差异：21 个文件，包含 roles_test.go；摘要所称 profile_test.go 实际不存在，Profile 测试在 roles_test.go。这不是单独阻塞。
- 原仓库：`C:/Zhy/code/go-rag`；复用审查目录：`C:/Zhy/code/go-rag-review-1d3`。`git worktree list` 与 common-dir 确认它是原仓库 linked worktree，共享 `C:/Zhy/code/go-rag/.git`，不是独立 clone。
- 切换前审查目录干净，无待搬移临时文件；历史证据及此前保存的临时测试保持原处。仅切换审查目录至 detached 目标，未切换、清理或修改原工作区分支、远端或配置。未 fetch。
- 未发现仓库/审查目录及适用祖先中的 AGENTS.md。已读通过的设计报告、方案、进度/变更、本批全部生产和测试文件、提交日志及相关锁定 Eino/SDK 源码。go.mod/go.sum 无本批变更。
- 先在干净目标完成全部验证，再加入审查方独立复现测试。未修改生产代码、方案、进度；未提交、推送或合并。

## 阻塞问题

### B1 · P1：预算输入和结算缺少 fail-closed 校验，账本可变负并重新放行

**目标源码**：`server/core/providers/budget.go:57`、`:75`、`:97`、`:108`、`:471`、`:485`。

**触发与后果**：

1. usageKnown 只检查任一分项或 TotalTokens 大于 0，就把整个 usage 当 known。`PromptTokens=-100, CompletionTokens=1, TotalTokens=1` 会使 InputUsed=-100。额度 100、已预留 10 的一次尝试结束后，额外输入 100 仍获准，而非保守保留或拒绝无效 usage。
2. 仅 TotalTokens=1、输入/输出分项为 0 时，两本分项账都按 0 结算，预留全部被释放。已有 total 与两分项不一致，不能据此认为分项真实为零。
3. 实际结算使用不受保护的 `+=`；两个已准入 lease 各返回 MaxInt64 输入后，InputUsed 溢出为 -2，再次预留成功。`canReserve` 在调用 exceeds 前也先计算 used+reserved，不能由后者兜住溢出。虽然这一数值是畸形响应边界，不是正常模型用量，但它证明所谓防溢出并未覆盖账本更新。
4. 进程构造器把负上限 sanitize 成 0（无限）；与请求/任务 options 拒绝负值相反。`NewProcessBudget(-1,-1,-1,-1)` 后输入/输出 100 的预留成功。配置错误直接关闭全部预算限制。

**复现**：`TestReviewBudget_InvalidUsageConservative`（negative-component、total-only）、`TestReviewBudget_SettlementOverflow`、`TestReviewBudget_NegativeProcessLimit`，全部在 `reproduction-final.log` 失败。

**最小修正**：进程和子作用域统一拒绝非法负值；明确 0 的配置语义。usage 必须先验证分项非负、完整性/total 一致性；无法可信结算时保留本次预留并报告异常，同时释放 slot。所有预留、结算及 used+reserved 计算做受检/饱和运算，溢出或已超额后拒绝进一步准入，不能回绕重新获得额度。不要求 token 粗估成为精确金额上限。

### B2 · P1：累计 owner 丢弃构造 context，deadline 每次重新计时

**目标源码**：`server/core/providers/budget.go:222`、`:238`、`:251`、`:276`、`:332`、`:339`。

**触发条件**：用请求 context 构造 RequestBudget，随后取消请求并调用公开 Reserve；或设置 WithDeadline，首个派生 context 已到期后，为同一 owner 再次调用 ContextWithDeadline。

**实际后果**：构造 ctx 被 `_ = ctx` 丢弃；Reserve 改用 Background，取消后仍成功预留。ContextWithDeadline 每次 `WithTimeout(ctx,d)`，为同一 owner 生成更晚的 deadline，随后 ReserveCtx 成功。请求/重试总期限可以刷新。TaskBudget 同样只复制 duration，后台移交也没有保留固定的预算期限语义。

**复现**：`TestReviewBudget_OwnerCancellation` 输出 Reserve=nil；`TestReviewBudget_AbsoluteDeadline` 等待第一个 context 的 Done 后观察到第二个 deadline 更晚、Reserve=nil。测试通过取消/Done 事件等待，没有用固定 sleep 假定完成。

**最小修正**：owner 持有固定的期限/取消约束；Reserve 和 ReserveCtx 都不能绕过 owner 的取消及过期状态。子调用、重试和同 owner 的派生 context 不重置总期限。明确 TaskBudget 同步→后台移交的阶段 context：保留累计预算及固定预算边界，执行 context 仍按 Stage 1 服务 root/task timeout 派生，不能把 HTTP 请求取消错误迁移到后台。无需消除所有检查后取消窗口；本例是取消已发生/期限已到仍继续准入。

### B3 · P2：Profile 身份隔离未封闭，缓存键存在确定碰撞

**目标源码**：`server/core/providers/profile.go:151`、`:179`；`registry.go:125`；`adapter.go:64`、`:75`、`:136`（其他 Wrap/Profile 方法同类）。

**触发与后果**：

- 两个经 NewProviderProfile 正式构造接受的 KeyOptions：`{"a":"1,b=2"}` 与 `{"a":"1","b":"2"}`，都编码成 `opts=a=1,b=2`。Registry 的 Chat 与 QA 返回同一个缓存实例。不同关键配置会误复用，排序并不能使未转义拼接无歧义。
- NewRegistry 直接保存调用方的 Profile 指针，适配器也保存并通过 Profile() 原样返回。构造 Registry 后更改外部 Profile.Model，首次构建看到的已是新模型。NewProviderProfile 对原始 map 的复制仅隔离原始 map，不能保护发布对象或 Registry/adapter 身份快照；缓存模型和 Profile 元数据可能脱节。该复现模拟配置对象被后续复用修改，不把它描述为已实测出的 race。

**复现**：`TestReviewProfile_CacheKeyCollision`（正式构造两份 Profile，证明实例相同）、`TestReviewProfile_RegistryOwnsSnapshot`（resolver/role 均看到构造后的外部变更）失败。

**最小修正**：采用稳定且无歧义的编码（例如规范结构编码或长度前缀；hash 也应基于无歧义序列）。Registry/Build/Wrap 边界拥有经过校验的防御性快照，避免通过 resolver 或 Profile() 暴露可写内部引用；或使用等效真正只读结构。保留相同身份共享缓存和初始化失败不发布的行为。不要仅要求调用方“不要改”来代替已批准的配置隔离边界。

### E1 · 验收证据阻塞：本批 race 未完成

两项独立 race 命令均 exit 1，输出 `exit status 0xc0000139`，尚未进入实际用例，因此不存在本轮十轮 race PASS 记录。

统一结论：**运行异常、race 未完成、根因待确认**。本机记录为 Go 1.27.1 windows/amd64、CGO=1、gcc 8.1.0，并不能仅凭此证明入口点异常根因，更不能证明代码没有 race。无需本轮反复排障或安装工具链；后续应在既有可运行环境补齐同包及关键用例 race 证据。普通并发重复不可代替 race。B1–B3 已独立足以阻塞，不依赖此环境结论。

## 实际装配及覆盖范围

本批提供的是以下两条分开的链，而非摘要图示的自动串联：

1. `NewRegistry(cfg.Budget)` 保存进程预算指针；调用方另行 `NewRequestBudget/NewTaskBudget → Reserve → Commit → Finish/Abort/Close`。
2. `Registry.Get* → Build*Role → openai.NewChatModel/NewEmbedder 或原有 rerank.NewRerank → 注入 HTTPClient → HTTP`。

registry.budget 未用于 get/build/角色调用或 Transport.Do。角色适配器直接委托 Eino；Transport.Do 直接调用 client.Do。没有自动 Reserve、重试执行器、response body 或 stream finalize 包装。包外也没有 providers 的业务引用。旧 Chat/Rewrite/QA、三处 Embedding、Rerank 仍走原路径。

**范围判断**：允许 2B 完成实际 HTTP/stream 预算接线，不因这些后续功能未实现单独要求本批扩成全 Stage 2；但本批只能称“可调用的计数/lease 核心与模型构造适配器”，不能称请求已受全链保护。进程共享依赖调用方复用同一个 cfg.Budget，Registry 本身不会在构造时新建预算，这是正确的基础；真实进程 owner/入口创建留待 2B。

未改业务配置和旧初始化链，因此本目标未在默认业务启用新 Embedding profile。BuildEmbedRole 作为库入口可以直接构造，它本身没有索引 guard；不能把本批视为已实现维度/身份门禁。2B 必须在 guard 就绪前保持生产启用关闭，不得只靠注释放行。2C 补实际 ES/Qdrant guard；无需在 2A 实现整个索引迁移。

Eino v0.6.0 的 Generate/Stream/EmbedStrings 签名、选项和泛型 reader 与适配器相容；普通构造/适配测试及 build 证明当前接口可编译。锁定 acl/openai v0.1.3 的 HTTPClient、Dimensions 字段与本批用法相符；未升级依赖。后续仍须在实际发送边界明确 SDK/transport 重试和重定向策略，不能把当前纯计数测试视为已覆盖隐藏 HTTP 尝试。

## 测试真实性

- 目标中实际提交 4 个测试文件：adapter_test.go、budget_test.go、registry_test.go、roles_test.go；未依赖原工作区未跟踪测试。
- 普通测试实际 20 个顶层用例、29 条 PASS（含子用例）。不把日志行数当用例数。
- ConcurrentReserve 的 sendCount 是测试在 Commit 后手动递增，不是服务器观察的 HTTP 次数。证明核心原子准入；不能证明所有实际发送会经过预算。
- SlotRelease_NonStream（budget_test.go:347）由测试 goroutine 自己 defer lease.Finish，并在 ReadAll/Body.Close 后执行；没有生产共享执行器自动把 body 生命周期绑定 lease。
- SlotRelease_StreamPaths（:392）把 Abort/Close/Finish 四个函数直接调用，证明 lease 终结核心而非真实 stream Close/取消路径。ConcurrentFinalize 验证 Close 与 Finish 争用 once，并非真实 reader EOF 与 Close 的接线验证。
- Fake_AllRoles 通过 FakeRegistry 自己的 getter 验证 fake，不能代替默认 Registry 的各角色装配测试。已有 adapter httptest 确实经过生产 BuildChatRole/BuildEmbedRole 和锁定 SDK，这是有效的模型构造证据。
- 新发现的无效 usage、owner 取消/期限、键碰撞在目标原有测试中没有覆盖。补测应进入提交，并经过生产共享内核；不必在 2A 强行补齐后续百炼全部协议测试。

## 独立实测：干净目标

实际 CWD 均为 `C:/Zhy/code/go-rag-review-1d3/server`，PowerShell；GOPROXY=off、GOSUMDB=off，不添加 online/integration 标签。只使用默认离线用例与 httptest 本机服务，未观察到真实 DB/ES/Qdrant/付费模型访问。执行器 `run-clean.ps1` 和每份日志记录完整命令、SHA、时间与真实退出码。

| 命令 | 退出码 | 结果 / 日志 |
|---|---:|---|
| go test ./core/providers/... -count=1 -timeout=120s -v | 0 | 20 顶层 / 29 含子项 PASS；ordinary.log |
| go test -race ./core/providers/... -count=1 -timeout=120s | 1 | 0xc0000139，未完成；race.log |
| go test -race ./core/providers/... -run 上述准确正则 -count=10 -timeout=180s -v | 1 | 0xc0000139，实际命中 0；race-stress.log |
| go test -p 1 ./... -count=1 -timeout=180s | 0 | 14 包 ok；full.log |
| go build ./... | 0 | build.log |
| go vet ./... | 0 | vet.log |

race 重复命令完整正则（已提交的 9 个顶层目标；不是实际运行成功）：

```
^TestBudget_(ConcurrentReserve_MaxRequests|DualScope_PartialFailure|RetryNoRefresh|TransferToBackground|IdempotentFinalize|ConcurrentFinalize|SlotRelease_NonStream|SlotRelease_StreamPaths|UnknownUsage)$
```

编译器路径 `C:/Program Files/mingw64/bin/gcc.exe`，版本输出 MinGW-W64 gcc 8.1.0，CC=gcc、CXX=g++、GOTOOLCHAIN=auto。见 environment.log。普通沙箱 gcc 启动还曾报 DLL 重定位异常，扩大沙箱权限的只读版本记录成功；未据此判定 race 根因。没有修改系统或工具链。

## 独立复现层（干净验证之后）

- 新增：`C:/Zhy/code/go-rag-review-1d3/server/core/providers/review_stage2a_test.go`。仅审查测试，不属于目标提交，保持未跟踪以供下轮保护。
- 保存副本：本目录 `review_stage2a_test.go`；保存完整新增 diff：`reproduction.diff`；哈希：`reproduction-sha256.txt`，SHA256 `A03090CDFD8549EE38C106DFC2B0FC7B4C6C49EE421A15418AFFA808FBE9E8C9`。
- `go test ./core/providers/... -run ^TestReview -count=1 -timeout=60s -v`：exit 1，7 个顶层复现全部失败（含两个 usage 子项）；最终日志 reproduction-final.log。失败断言表达的是应满足的契约，不是为使当前实现通过而改生产代码。
- reproduction.log 保留首次结果；最终文件增加了正式 NewProviderProfile 构造步骤，排除仅用原始未校验对象造成碰撞的解释。
- 干净目标验证与复现层分开，未将复现加入后的树称为干净目标。没有复制实现者未提交测试。

## 实现者历史证据核对

- 已提交 test-budget-stress.log 的真实正则为 `TestBudget_Concurrent|TestBudget_SlotRelease`，只命中 4 个顶层：ConcurrentReserve_MaxRequests、ConcurrentFinalize、SlotRelease_NonStream、SlotRelease_StreamPaths，各 10 次，合计 40 顶层 PASS；StreamPaths 的 4 子项×10 再加 40，故 80 条 PASS。
- 该 80/80 不是 8 个独立预算场景完整重复，更没有覆盖 DualScope_PartialFailure、RetryNoRefresh、TransferToBackground、IdempotentFinalize、UnknownUsage 的十轮重复，也不是 race。
- 历史 race 日志证明运行异常，不能支持摘要“确定非代码缺陷/确定缺入口点”的根因判断；本轮不重复系统排障。
- 历史普通/full/build/vet 的成功与独立重跑相符；不据此推出无并发缺陷。

## 非阻塞建议与后续边界

- TaskBudget.reserveCtx 的 transferred 检查和实际预留分两次锁，需在未来接线明确移交与并发准入语义；本轮不把相互重叠调用的一般窗口单列阻塞。
- capabilities 与 KeyOptions 当前主要是元数据，BuildChatRole 不按 Protocol 选择实现；2B 应明确支持矩阵、拒绝不支持的配置并落实 option 传递，不把统一 transport 当统一协议。
- 2B 补共享发送执行/重试及 body/stream 终结接线时，测试在服务器端观察实际发送，覆盖阻塞 body、读错、提前 Close、取消、EOF/Close 争用与缺 usage。不要复制执行编排到测试后再宣称生产接线已证。
- 2C 的身份/维度校验及既有索引保护、新 profile 启用门禁继续保留；2D 仅在凭据和费用授权后执行。实际 usage、服务能力、A-4、ES 启动兼容、SQL driver 取消均未在本轮验证。
- 不更新 Stage 2/Stage 1 状态；本轮报告完成后停止，等待实现者修复的新完整 SHA。
