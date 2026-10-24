# Stage 2B 增量独立复核：02d6bae0

**结论：存在阻塞，不能验收 Stage 2B。** 干净目标的构建、vet、全量和受影响包race已通过；原四组问题中，提交完整性、任务期限和初始化并发发布已闭合。仍有两个生产接线阻塞：消费者Close没有接到真实源关闭，以及ChatStream检索/回答阶段没有同一最外层request owner。

日期2026-10-20。基线 **06ef59c68000059f29302710ea15ce93afdc88bd**；目标 **02d6bae0ce75f7f6207d9bf37d672dc6033285c8**。对象存在，基线为祖先；检查18文件完整增量及原阻塞相邻调用链。没有重做无关历史审计。

## 目录保护与分层

复用 C:/Zhy/code/go-rag-review-1d3 detached linked worktree，common dir C:/Zhy/code/go-rag/.git。原仓库分支未切换/清理，未clone/fetch/改Git配置；未发现适用AGENTS.md。切换前十二份原复现保存到preserved/并记录SHA256，历史证据未覆盖。

正式命令先在干净目标执行完毕，随后加入独立测试。本轮不再需要overlay或未提交生产依赖。最终生产文件git diff --exit-code -- server为0；十三份review文件为未跟踪测试，源/hash/diff存repros/，final-worktree-status.txt保留状态。

证据分为：

1. **干净目标自带测试**：ordinary/race-providers/race-wiring/race-key-stress/full/build/vet.log及run-clean.ps1。
2. **目标＋独立复现，无overlay**：target-plus-review.log、race-review.log、race-review-openai-close.log。生产字节均来自固定目标，不把review测试称为提交自带。
3. **实现者日志与未提交测试**：verification/stage2b-fix5/；原仓库server/core/providers/stream_close_test.go未提交，只读保存源/hash到implementer-uncommitted/，本轮没有复制到目标或执行它。

未修改生产代码、方案、进度或验收状态，未提交/推送/合并。

## 阻塞1 / P1：新增Close方法仍未接到消费者返回值，阻塞流占用不释放

目标位置：

- server/core/providers/bailian/chat.go:240–254、270–293。
- server/core/providers/budget_adapter.go:365–380、394–417。

两个内部包装类型现在有Close方法，会尝试inner.Close并等待done。但是Stream实际返回的是schema.Pipe创建的reader；调用者拿到的是 *schema.StreamReader，而不是内部leaseStreamReader/budgetStreamReader。调用返回reader.Close只关闭pipe接收端，没有调用这些新增Close方法。转发goroutine若正阻塞源Recv，无法到达Send，也不会执行defer或关闭done。

这不是“等100ms不够”的问题，而是控制链未接线；扩大等待时间不能让未被调用的方法生效。直接调用内部Close的单测不能证明返回给业务方的流已受保护。

**真实HTTP证据，无overlay**：

- TestSupplementStreamEarlyCloseFinalizes：百炼实际Stream，服务发首chunk后保持连接；消费者读首条后关闭返回reader，InFlight仍1、InputReserved=2、OutputReserved=8192。只有测试额外取消请求/释放服务后资源才收敛。cleanup显式等待handler退出和slot归零。
- 同条件 TestIndependentOpenAIStreamEarlyCloseFinalizes 经providers.BuildChatRole通用预算链复现，排除只改百炼的遗漏。
- 百炼关闭用例-race-count10：10/10断言失败；通用OpenAI关闭用例另跑-race-count10：10/10断言失败。均非DATA RACE，均非环境启动异常。

后果：用户停止消费但服务不继续发数据时，连接、转发任务和预算slot继续持有；可能阻断后续请求。done仅在goroutine退出时通知，不能促使它退出。

最小方向：在实际返回的Eino流边界建立可实现的Close/取消→源请求/读取中断→lease终结→任务退出链。先核实锁定Eino v0.6.0的公开API；它是具体struct，不能凭新增同名方法假设存在Close代理。必要的接口/适配设计应明确说明，勿使用生产unsafe反射强改SDK内部。保留多chunk、EOF/错误/提前Close的保守usage、幂等与Stage1 SSE。不能把手动关闭上游或外部取消作为“消费者Close已完成”的证据。

## 阻塞2 / P1：ChatStream的检索和回答仍创建不同request owner

目标位置：

- server/internal/controller/rag/rag_v1_chat_stream.go:17–18、45–59。
- server/core/retriever.go:64。
- server/internal/logic/chat/chat.go:281。
- server/core/common/budget_ctx.go:17–34。

WithRequestBudget已经正确保留ctx中存在的owner，但真实ChatStream入口只WithCancel，没有创建预算owner。它把同一个无预算ctx分别传给retrieve和answerStream：Retrieve局部派生预算ctx不会回传controller；回答入口又从原ctx创建另一份。两个兄弟阶段仍不共享累计请求账本/固定期限。

**证据**：TestIndependentControllerSharesOwnerAcrossStages调用真实ControllerV1.ChatStream，使用其既有实例注入边界排除真实检索/History设施；两个边界调用同一生产common.WithRequestBudget，记录到不同RequestBudget指针。没有复制预算或controller编排实现。普通失败，准确-race-count10十次全部失败。结合上列真实默认入口代码可确认生产路径同样分离。此测试不是完整模型/向量库集成探针。

后果：不能对整次Retrieve→Rewrite/Rerank→最终Generate实施统一子作用域累计额度和deadline；调用阶段切换后新owner从零开始。ProcessBudget仍限制进程总额，本报告不把该问题夸大为“进程额度也被重置”。已有caller显式注入owner可工作，但普通生产入口未执行此步骤。

最小方向：在实际最外层ChatStream入口创建一次request owner并传给两个阶段，保留controller派生取消和Stage1 cleanup/SSE契约；内层入口复用而不刷新。用真实controller共享路径验证两个阶段身份及累计限额，而非只在模型适配器单测中手工注入ctx。

## 原四组问题核对

| 项目 | 本轮结论 |
|---|---|
| 提交完整性/Config.Copy | Registry字段及Copy已进入对象；干净build/vet/full通过，闭合 |
| Stream Close | 新方法未接返回reader，原真实关闭复现仍失败，未闭合 |
| Task owner/deadline | 共享scope、固定绝对期限、opts只能收紧与WithTaskBudget错误返回源码正确；原期限/嵌套/scope复现通过。该子问题闭合，整ChatStream最外层owner仍见阻塞2 |
| 初始化并发发布 | core.New移除全局SetRegistry/rollback，QA/Chat显式使用conf.Registry。原串行失败、并发读半成品/回滚覆盖newer复现全部通过，闭合 |

保持通过：opts输出预留同源、默认/注入Client重定向禁止、per-call Embedding usage、OpenAI预算、Send语义/多chunk、Index累计scope、Reset超时Registry保留/身份校验、显式Chat cfg、Rerank错误传播、nil输入/Client、原始Embedding index、生产Embedding fail-closed、builder同步冻结。不因这些正确部分要求重做。

## 实际生产装配与边界

ProviderConfig→buildRegistry（持久ProcessBudget）→Registry→builder→百炼独占预算适配或通用budget*Role→锁定SDK/旧Rerank HTTP。百炼Embedding/Rerank关闭内层重复预算包装。默认Chat/Rewrite/QA通过Registry，显式Chat cfg保持兼容选择。

core.New现在使用传入Registry完成模型构造，工厂成功后发布；不再临时切全局。Index同步/后台共享task scope及固定deadline。Retrieve与回答入口各自使用WithRequestBudget，最外层ChatStream共享尚缺。流生成有预算，消费Close路径仍存在阻塞1，不能把账本核心正确等同于全生命周期接线正确。

## 干净目标独立实测

CWD C:/Zhy/code/go-rag-review-1d3/server。Go1.27.1/windows/amd64，GCC16.2.0 MSYS2 UCRT64、binutils2.47.20260726，CGO=1、GOFLAGS空；仅进程PATH/CC/CXX设置，GOPROXY/GOSUMDB=off。无依赖升级/系统修改。未连接真实DB/ES/Qdrant/模型，HTTP仅本机httptest。

| 命令范围（完整参数见日志/run-clean.ps1） | EXIT | 结果 |
|---|---:|---|
| 受影响包普通测试 | 0 | 266顶层PASS，291RUN含子测试 |
| go test -race ./core/providers/... -count=1 -timeout=180s -v | 0 | 三包，106顶层PASS/120RUN含子测试，无DATA RACE |
| common/rerank/logic-rag/chat race | 0 | 四包实际执行通过，无DATA RACE |
| providers关键准确正则-race-count10-v | 0 | 370RUN/370顶层PASS，无DATA RACE |
| go test -p 1 ./... -count=1 -timeout=180s | 0 | 串行全量通过；该命令非-v，不能以无RUN文本说未执行 |
| go vet ./... | 0 | 通过 |
| go build ./... | 0 | 通过 |

本轮不再有提交编译缺口或旧race工具链异常。370为37个包限定用例各10轮，裸名称36个（RetryNoRefresh跨包重名）。名称和完整正则见race-key-stress.log；这些提交内用例通过不覆盖两个独立失败条件。

## 独立复现层（无overlay）

- target-plus-review.log：25顶层，23PASS/2FAIL，另有index三个子测试；失败是百炼提前Close及实际controller跨阶段owner。旧十二份测试按来源保留，仅review_owner_test.go适配WithTaskBudget新增error返回并断言错误；新增controller测试。
- race-review.log：六用例×10，60RUN/40PASS/20FAIL，DATA RACE=0。任务期限、原嵌套owner/共享scope及初始化并发四项各10PASS；百炼关闭和controller owner两项各10FAIL。
- race-review-openai-close.log：新增通用OpenAI实际流关闭复现，10RUN/10FAIL，DATA RACE=0。新增函数与百炼复现共用相同服务/cleanup模式，仅换真实BuildChatRole构造。最后此新增函数独立十轮运行，未声称前面的25顶层日志也已包含它。

所有测试源/差异/hash位于repros/；历史原版在preserved/。不修改生产代码使测试通过，也不把“0DATA RACE”当作行为正确。

## 实现者新增测试交付缺口

原工作区server/core/providers/stream_close_test.go未跟踪、不在目标树。它包含5项：CancelWakesSource、WaitGoroutineExit、SharedKeepsDeadline、SharedOptsTightenOnly、WithTaskBudget_ErrorPropagation。实现者130次race命中13项，包括此文件；提交内只能找到其中8个已有Stream用例，新增5项不能算目标自带验收。原文件只读保存源和hash到implementer-uncommitted/，未执行。

该文件部分测试手工构造内部包装/复制转发循环，不能代替实际返回reader.Close路径。建议核实归属并补交正确、必要的回归，避免只补提交一个仍不触发真实缺陷的测试。合并为一项交付证据缺口，不拆五个问题、不为归档格式单开修复轮次；本轮实际正确性阻塞仍是上述两项。

## 后续门禁及非阻塞项

2C保留六处维度、索引fingerprint及模型身份guard；新Embedding profile生产启用继续fail-closed。2D授权在线探针未执行。A-4、真实ES/Qdrant/SQL取消及Stage1历史补证不扩大。本轮依赖和协议未变，官方资料沿用independent-2b-a24cb8d/SOURCE_EVIDENCE.md；流API以本地锁定Eino源码核实，未猜测不存在的配置/API。

无命名或一般文档阻塞。建议Qoder专家模式集中修复两个接线问题及必要测试交付；见QODER_PROMPT.md。停止等待新SHA，不更新验收状态、不宣布Stage2完成。
