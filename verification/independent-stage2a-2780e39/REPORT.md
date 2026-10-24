# Stage 2A 阻塞修复增量独立复核

**结论：存在阻塞，暂不能验收 2A。** 原 7 个复现经必要 API 适配后全部通过；B1 与 B3 仍各有一处同契约遗漏，B2 的本批 owner 准入/固定期限问题已闭合。E1 race 仍未完成。

## 固定范围和保护

- 目标：`2780e398f2a3091f60c0d6c34ce7661e1716244d`。
- 增量基线：`5bf62f0069a527b4940b8336f3cb953ccb551813`，已核实为目标祖先；结合已通过设计 `212ccd8cc4c5743e6e34dfe5cb73e02b7a794b84` 与前次完整源码复核。
- 日期：2026-10-19；CWD（测试）：`C:/Zhy/code/go-rag-review-1d3/server`，PowerShell。
- 继续复用同一 linked worktree，未 clone、fetch、切换/清理原工作区或更改配置。目标相对基线 21 文件、+1492/-135；已审完整增量、生产调用关系与新增测试，不重开 Stage 1。
- 适用 AGENTS.md 沿用前轮核验，本次审查目录内也未发现新增 AGENTS.md。已读前轮独立报告、本批生产/测试改动、方案和进度/变更及本批提交验证报告。go.mod/go.sum 未改。
- 切换前唯一临时文件 review_stage2a_test.go 已移动保存至本目录 `preserved/`；哈希 `A03090CDFD8549EE38C106DFC2B0FC7B4C6C49EE421A15418AFFA808FBE9E8C9` 与上轮一致。其余历史证据未覆盖。切换后目标干净，先完成干净验证，再加入复现测试。

## 原问题闭合情况

| 原项 | 本轮结论 | 证据 |
|---|---|---|
| B1：负 usage、不完整 total、结算溢出、负配置 | 原复现均修复；整个算术防护契约尚未完全闭合 | usageKnown 校验、finalize 受检结算、构造器返回错误有效；剩余 R1 见下 |
| B2：owner 取消与期限刷新 | 本批准入及固定期限问题已闭合 | 原 OwnerCancellation、AbsoluteDeadline 通过；提交内 Request/Task 期限测试通过；有效 root-owner 装配补证通过 |
| B3：键碰撞和外部 Profile 引用 | 原复现均修复；内部身份封闭尚未完全闭合 | 长度前缀编码消除原碰撞，Registry/Build/Wrap 克隆、Profile() 深拷贝有效；剩余 R2 见下 |
| E1：race | 未闭合 | 普通包与关键十四项重复的 race 命令都启动异常，实际执行 0 个用例 |

本轮没有因新命名或一般文档差异增加阻塞。以下 R1/R2 是前轮已要求的“所有预留运算防溢出”和“resolver 不暴露内部引用”的遗漏，不要求实现后续百炼协议或全业务预算接线。

## 剩余阻塞

### R1 · P2：预留阶段仍有未检查的加法，合法 estimate 可使 Reserved 回绕为负

**目标位置**：`server/core/providers/budget.go:67`、`:71`、`:79`、`:87`。

**调用链**：RequestBudget.Reserve → reserveCtx → scope.reserve → 两作用域 canReserve → applyReserve。

**触发条件**：token 上限为 0（公开 API 的合法无限配置），保留两次未结束的输入预留，每次 InputTokens=MaxInt64。attempt 上限 10、并发上限 3，无负输入或非公开状态修改。

**证据**：`TestReviewFix_ReservationOverflow`：

```
before: AttemptsUsed=1, InFlight=1, InputReserved=9223372036854775807
after:  AttemptsUsed=2, InFlight=2, InputReserved=-2
second Reserve error=<nil>
```

canReserve 只检查已有 used+reserved，未检查再加本次 estimate；exceeds 在 limit<=0 时直接返回 false。applyReserve 的 `inputReserved += e.InputTokens` / output 同类仍不受保护。因此新 addChecked 没有覆盖完整预留事务。

**实际后果与边界**：进程与子作用域可发布负预留账本，违反非负、保守记账和溢出拒绝契约。本例涉及极端合法整数及无限 token 配置；不把它夸大为已证明有限 token 上限可绕过或真实费用超支。零表示不限也不能允许计数回绕。

**最小修正**：在同一锁内对每个作用域的 attempts、slot、used+reserved+estimate 及实际待更新值做完整受检预计算；任一无法表示时零变更拒绝，随后才整体应用。不要只在有限上限的比较分支检查溢出。补输入/输出、一个作用域可容纳而另一个不能容纳时的完整回退断言。

### R2 · P2：CredentialResolver 仍收到内部 Profile 指针，可破坏缓存键与模型的一致性

**目标位置**：`server/core/providers/registry.go:199`、`:207`、`:211`、`:215`、`:220`、`:289`、`:290`。Embedding/Rerank 的 resolve 路径同类。

**调用链**：getChatRole 先以内部 p 计算 key → buildChat → resolve → r.creds(ctx,p) → BuildChatRole 使用同一个 p → 缓存发布到旧 key。

**触发条件**：注入的 CredentialResolver 修改收到的 Profile（例如解析过程中补充/归一化字段），或保留该指针后修改。回调目前收到 Registry 自己持有的对象，而非只读快照。

**证据**：`TestReviewFix_ResolverCannotMutateRegistryIdentity` 使用合法配置 original-model；resolver 把传入的 p.Model 改成 resolver-modified-model，只返回离线 key。GetChat 构建成功后 Profile().Model 为 resolver-modified-model，而缓存 key 在 resolver 修改之前已按 original-model 算出。构造没有模型 API 调用。

**实际后果**：配置身份仍可被扩展回调改写，模型可在错误身份键下发布；外部原始 Profile 和 Profile() 返回值的复制无法封闭这个出口。这不是安全攻击结论，也不声称已跑出 race；是可复现的配置隔离与缓存一致性缺陷。

**最小修正**：resolver 只拿防御性深拷贝或真正只读视图；构建与缓存身份始终基于 Registry 的同一不可变内部快照。补回调修改字段/map、回调持有副本后修改及缓存命中不变的测试。无需另造 Provider 框架。

### E1 · 必要验收证据仍缺 race

两项独立 race 命令都 exit 1，`exit status 0xc0000139`，未进入测试主体。统一记录：**运行异常、race 未完成、根因待确认**。gcc 8.1.0 与错误同时出现不足以确认因果；不把普通重复测试代替 race，也不判定代码已无竞态。

本轮没有安装、升级工具链或反复系统排障。B1/B3 的剩余正确性问题与 race 环境无关，已由普通离线复现确认。

## B2 已修复及 2B 装配边界

RequestBudget 现在在准入时检查 owner context 和构造时确定的绝对期限；ReserveCtx 不再能用未取消的参数 context 绕过 owner。相同 owner 的 ContextWithDeadline 不再刷新配置期限。TaskBudget 移交共享 scope 及固定 deadline。

正确的后台用法是 **NewTaskBudget(serviceRoot)，同步 ReserveCtx(requestCtx)，移交后 ReserveCtx(taskCtx)**。`TestReviewFix_TaskRootOwnerSurvivesRequestCancel` 验证同步请求取消后后台还能准入，服务 root 取消后准入返回 Canceled。

TransferToBackground 原样保留 ownerCtx，因此不能先把 TaskBudget 的 owner 绑到 HTTP 请求再期待移交自动剥离取消。2B 应固定上述服务 root owner 装配，并确保实际执行 context/派生 context 受阶段取消及期限控制。ContextWithDeadline 目前仅合并传入 ctx 与配置的 ownerDeadline，不会自动桥接不同的 ownerCtx；本批证明的是准入约束，不能据此宣称 HTTP/stream 中途取消已经接好。不将未实现的 2B 接线单列本批新增阻塞。

## 干净目标独立验证

环境：Go 1.27.1 windows/amd64，CGO_ENABLED=1，CC=gcc，CXX=g++，GOTOOLCHAIN=auto；GOPROXY=off、GOSUMDB=off。完整 C 编译器路径/版本在 environment.log。只运行默认离线测试与 httptest；未观察到真实设施/付费模型访问，无 online/integration 标签。

| 完整命令 | 退出码 | 实测 / 日志 |
|---|---:|---|
| go test ./core/providers/... -count=1 -timeout=120s -v | 0 | 30 顶层、44 含子项 PASS；ordinary.log |
| go test -race ./core/providers/... -count=1 -timeout=120s | 1 | 0xc0000139，未完成；race.log |
| go test -race ./core/providers/... -run 下列正则 -count=10 -timeout=180s -v | 1 | 0xc0000139，命中 0；race-stress.log |
| go test -p 1 ./... -count=1 -timeout=180s | 0 | 14 包 ok；full.log |
| go build ./... | 0 | build.log |
| go vet ./... | 0 | vet.log |

准确关键正则（14 个提交内预算用例；因启动异常没有实际执行）：

```
^TestBudget_(NegativeUsageConservative|TotalOnlyUsage|SettlementOverflow|NegativeProcessLimit|OverflowSlotRelease|OwnerCancelPropagates|DeadlineNotRefreshedByRetry|TransferKeepsAbsoluteDeadline|ConcurrentReserve_MaxRequests|DualScope_PartialFailure|RetryNoRefresh|TransferToBackground|IdempotentFinalize|ConcurrentFinalize)$
```

执行脚本 run-clean.ps1；actual-test-names.txt 为普通测试实际命中名称；各日志含完整 SHA、CMD、CWD、时间和真实退出码。先完成全部干净命令，再加入审查方复现；未把补测后的树称为干净目标。

## 独立复现及保护清单

- preserved/review_stage2a_test.go：上轮原文件，仅保存，未直接原样混入新目标。source-hash.txt 保存来源与原 SHA256。
- review_previous_test.go：原 7 个复现的 API 适配版。仅将 NewProcessBudget 双返回值经检查 helper 适配；负配置改为检查构造错误；Profile() 断言适配值类型。没有改变原契约断言来迁就业务结果。repro-api-adaptation.txt 保存差异。
- review_remaining_test.go：新增两个失败复现及一个有效 task-root 装配成功补证。
- 两份测试在干净验证后复制到审查目录 `server/core/providers/`，均未跟踪、未提交。完整新增 diff 为同名 `.go.diff`；test-hashes.txt 保存哈希，final-worktree-status.txt 记录目标及这两个新增文件。没有修改生产代码。
- 命令：`go test ./core/providers/... -run ^TestReview -count=1 -timeout=60s -v`；exit 1。原 7 顶层通过，新 task-root 用例通过；预留溢出和 resolver 身份隔离两个用例失败。日志 reproduction.log。

## 实现者证据核对

提交内确有新增修复测试，不依赖原工作区未跟踪文件。普通日志 30 顶层/44 含子项的数量与独立结果一致。历史 test-key-stress.log 确认 140 顶层 RUN、230 含子项 RUN，来自 14 个顶层×10；它是普通重复而非 race。实际名单在实现者 REPORT.md；其中没有 TotalOnlyUsage、SettlementOverflow、DualScope_PartialFailure、RetryNoRefresh、IdempotentFinalize 的十轮重复，不能按“所有预算回归均十轮”解读。

实现者 verification/stage2a-fix/REPORT.md 的 race 小节仍写确定的 MinGW 缺入口点根因，与摘要“根因待确认”不一致。这里只校正证据结论，不因这一文案另立修复轮次。

## 实际生产链与后续门禁

本批依旧没有默认业务 providers 引用；预算 owner/lease 核心与 Registry→BuildRole→SDK/Rerank→HTTP 的构造链分开，未自动绑定 HTTP/stream finalize。此边界符合 2A/2B 分工，不能宣称已有全应用预算覆盖。

默认工厂和实际业务接线、HTTP 尝试/重试计数、body/stream 生命周期绑定由 2B 承接；新 Embedding profile 不得在维度/身份 guard 缺席时生产启用。维度/fingerprint/旧索引保护留待 2C，在线协议/usage/服务能力按授权 2D 探针处理。A-4、真实 ES/Qdrant、SQL driver 取消等本轮未验证。不升级依赖、不迁移数据、不调用模型 API。

## 交付决定

修复 R1/R2、保留本轮确认通过的原用例，并补齐必要 race 证据后再提交完整 SHA 供增量复核。建议 Qoder 专家模式处理预算算术和缓存边界；不需要展开全项目审计或追加未来阶段实现。

本轮未修改生产代码/方案/进度，未提交、推送或合并；报告完成后停止。
