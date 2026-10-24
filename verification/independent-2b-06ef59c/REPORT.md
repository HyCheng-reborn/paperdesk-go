# Stage 2B 增量独立复核：06ef59c6

**结论：存在阻塞，不能验收 Stage 2B。** 原owner累计scope与串行初始化失败回滚有所修复；提交完整性、流提前Close、任务固定期限和并发初始化发布仍未闭合。

日期2026-10-20。基线 **64aac44155414403d5d4e007863987e1f79acfe2**；固定目标 **06ef59c68000059f29302710ea15ce93afdc88bd**。对象存在且基线为祖先；检查17文件完整增量及四项原阻塞相关调用链，未扩大无关历史审计。

## 工作区和证据

复用detached linked worktree C:/Zhy/code/go-rag-review-1d3，common dir C:/Zhy/code/go-rag/.git。主工作区未切换/清理，未clone/fetch/改Git配置。未发现适用AGENTS.md。切换前十份独立测试保存至preserved/并记录SHA256。历史报告未覆盖。

正式命令全部在加入复现前执行。最终生产文件git diff --exit-code -- server为0；十二份review测试保留为未跟踪文件，副本、hash和diff位于repros/，状态见final-worktree-status.txt。未修改生产代码、项目方案或验收状态，未提交/推送/合并。

证据分层：

- 干净目标：ordinary、race-providers、race-wiring、race-key-stress、full、build、vet.log及run-clean.ps1。
- 目标＋独立测试（无overlay）：target-plus-review.log、race-review.log。生产实现完整来自目标对象。
- 目标＋原仓库未提交Config快照＋独立测试：overlay-logic.log、race-overlay-init.log。原始config.go只读保存到supplement-sources/，由overlay.json映射编译，不覆盖目标；哈希和original-uncommitted.diff可追溯，不算提交自带通过。
- 实现者日志：verification/stage2b-fix4/，其全量/build/vet通过无法由目标树重现。

## 阻塞（行号指目标提交）

### B1 / P1：仍未提交Config.Registry，干净对象不能构建

位置：server/core/rag.go:91、93、97；server/core/config/config.go:12；server/internal/logic/rag/retriever.go:105。

core.New读取conf.Registry，工厂传Registry，但目标Config没有该字段。go build实际报 `conf.Registry undefined`，vet/全量/依赖core的logic包同样失败。原仓库config.go仍有真实未提交+5行（providers导入及Registry字段），不是CRLF幻影；本轮没有把此文件内容混入正式验收。

影响：最基本的完整交付与构建门禁未满足。Index预算代码本轮已提交，不再列为遗漏；Config遗漏合并为一项。

最小方向：选择性补齐正确生产定义及必要依赖，在干净提交对象验证，先证明对象可编译再汇报通过。不能继续用主工作区未提交依赖跑出的日志替代交付。

证据：build.log、ordinary.log、vet.log、original-uncommitted.diff。

### B2 / P1：退出defer无法唤醒阻塞Recv，流提前Close仍泄漏占用

位置：server/core/providers/bailian/chat.go:262–287；server/core/providers/budget_adapter.go:384–410。

本次只把源Close移到转发goroutine的defer。下游返回的是独立pipe reader，Close后不会直接关闭源；goroutine若在inner.Recv阻塞，尚未退出，defer根本没有机会执行。依赖后续writer.Send感知关闭的结构没有改变。

原真实SSE复现 TestSupplementStreamEarlyCloseFinalizes（无overlay）：读首chunk后服务不再发数据，消费者Close后slot仍InFlight=1、InputReserved=2、OutputReserved=8192，只有测试显式外部取消/释放服务才退出。cleanup等待handler及slot收敛。普通失败，-race-count10十次全部失败，无DATA RACE。

影响：停止消费仍占模型连接、并发槽和转发goroutine，阻断后续请求。通用OpenAI和百炼两处同构实现均有该路径。

最小方向：Close/取消必须有直接打断真实源/请求的机制，并对lease和转发任务进行幂等、可等待的收敛；不能把“退出时清理”当作“促使退出”。保持EOF/终止错误/提前Close的usage规则及Stage1 SSE契约。

提交 TestStream_BlockedSourceClose 主动调用srcWriter.Close，额外替生产关闭源，仍不能证明所称关闭所有权链；本次未新增针对原真实阻塞复现的提交内测试。原Send语义/三chunk完整性已修复，保持通过，不重开此项。

### B3 / P1：共享任务账本保住累计额度，却丢失固定绝对期限；顶层请求链仍未覆盖

位置：server/core/providers/budget.go:432–453（NewSharedRequestBudget）；server/core/common/budget_ctx.go:17–34、53–67；server/core/indexer.go:82、174–181。

NewSharedRequestBudget仅共享tb.s，却从新opts重新构造deadline和ownerDeadline。后台调用不带opts，因而ownerDeadline变成零；带opts会从time.Now重新计时。移交本应保持的固定任务边界被丢失，适配器实际消费的是这个新RequestBudget视图。

无overlay最小复现 TestIndependentSharedTaskKeepsDeadline：创建20ms任务，通过其真实ContextWithDeadline等待到期；tb.Reserve确认DeadlineExceeded；随后创建共享视图rb并Reserve却成功。普通失败，-race-count10十次全部失败。测试等待context事件，不用固定sleep猜任务完成。

影响：过程累计账本虽不刷新，固定deadline仍可通过共享视图/后台移交绕过。不是要求消除检查后所有取消窗口，而是已明确过期的任务仍可新准入。

最小方向：共享视图继承固定绝对deadline，额外配置至多收紧；后台执行ctx从服务root派生与保留任务绝对边界分开处理。检查过期/移交句柄语义，不为允许后台脱离HTTP取消而刷新任务期限。预算创建/移交失败必须显式传播；fmt.Printf后返回ctx,nil并非“返回错误”。

原TestIndependentNestedEntryKeepsOwner和TestIndependentTaskUsesTransferredScope本轮均通过，十轮各10PASS：嵌套复用和同步/后台累计scope修复可确认。

此外真实controller ChatStream仍先用ctx调用retrieve，再用同一原ctx调用answerStream；Retrieve在局部派生ctx，模型回答入口又创建owner，controller没有最外层owner注入。嵌套helper保留ctx已有值，并不自动让两个兄弟阶段共享。该点是源码装配边界：server/internal/controller/rag/rag_v1_chat_stream.go:17–59；core/retriever.go:64；internal/logic/chat/chat.go:281。原“整请求覆盖Retrieve/Rewrite/Rerank/Generate”的契约仍需实际最外层接线测试，不要求扩大到其他阶段。

### B4 / P1：临时全局发布仍可见，无条件失败回滚覆盖并发新Registry

位置：server/core/rag.go:89–100；server/internal/logic/rag/retriever.go:106–114。

core.New仍在索引验证前SetRegistry新值。失败defer无条件恢复oldRegistry，既未隔离构造上下文，也未核实当前引用/实例代次。单次调用失败后旧值可恢复，但等待期间其他请求看到未完成配置；若其他拥有者发布新值，旧构造失败时把它覆盖。

在仅补齐Config字段的overlay层 TestIndependentInitRollbackPreservesNewerRegistry：真实工厂→core.New→本机ES HEAD处理；handler先读取全局，观察到未完成Registry，再模拟另一拥有者发布newer并返回401；失败回滚后全局不是newer。两项断言都失败，-race-count10全部失败，无DATA RACE。此层不算目标自带测试，也未连接真实ES。确定事件在HTTP初始化等待点交错，不靠sleep。

原TestReviewRegistryPublicationOnInitFailure（无并发）本轮通过，故明确为串行回滚已修复，并发发布契约未闭合。

最小方向：新实例通过显式Registry/模型依赖完成局部构造，成功才由实例持有者发布；失败不改旧全局。不以临时全局切换模拟依赖注入，不只追加不具身份保护的回滚。发布/清理遵守既有锁和代次规则，不持锁跨长初始化等待。

## 原问题状态与实际生产链

| 项目 | 结论 |
|---|---|
| 提交完整性 | Index接线已入提交；Config字段仍遗漏，未闭合 |
| 流Close | defer只补退出清理，原真实阻塞复现仍失败，未闭合 |
| owner累计scope | 原嵌套/Task scope复现通过；固定期限及最外层整请求边界仍缺 |
| 初始化发布 | 串行失败恢复旧值通过；并发可见性/回滚覆盖新值未闭合 |

保留已正确修复：opts输出上限同源、OpenAI生成预算、默认/注入Client重定向禁止、per-call Embedding usage collector、Send语义/多chunk、显式Chat cfg、Rerank错误传播、nil Transport/nil doc、原始Embedding index验证、Embedding生产fail-closed、builder同步冻结、Reset超时引用保留及身份校验。原复现均未发现这些项需再次修改。

实际链：ProviderConfig→持久processBudget/buildRegistry→Registry→百炼预算适配或通用budget*Role→SDK/旧Rerank HTTP；Index现已调用WithTaskBudget并在后台以共享视图消费，累计scope正确，期限如B3。Chat默认模型路由已迁移，但整ChatStream最外层owner尚未统一。core.New通过临时全局切换初始化，B4阻止宣称生命周期接线完成。

## 独立验证结果

CWD C:/Zhy/code/go-rag-review-1d3/server；Go1.27.1/windows/amd64，MSYS2 UCRT64 GCC16.2.0、binutils2.47.20260726，CGO=1、GOFLAGS空。仅进程PATH/CC/CXX配置，GOPROXY/GOSUMDB=off。未安装/升级工具链、改系统或Git配置。未调用真实模型、DB、ES/Qdrant，所有HTTP均为httptest。

| 干净目标命令（全参数见对应日志/run-clean.ps1） | EXIT | 实际结果 |
|---|---:|---|
| 受影响包普通测试 | 1 | 部分包通过，core/logic-rag因Config字段失败 |
| go test -race ./core/providers/... -count=1 -timeout=180s -v | 0 | 三包实际执行通过，无DATA RACE |
| common/rerank/logic-rag/chat race | 1 | logic-rag编译失败，不扩大部分包通过 |
| providers关键准确正则-race-count10-v | 0 | 370RUN/370顶层PASS、0FAIL、0DATA RACE |
| go test -p 1 ./... -count=1 -timeout=180s | 1 | 编译失败 |
| go vet ./... | 1 | 编译失败 |
| go build ./... | 1 | 编译失败 |

370为37个包限定用例各10轮，36个不同裸测试名（RetryNoRefresh跨两包同名）。正则及实际名称在race-key-stress.log，不包含无法编译的logic-rag生产初始化验证；不是完整2B验收。实现者本批100PASS仅命中已有10个流/owner用例，未触达此次仍失败的独立关闭、任务固定期限或并发回滚条件。

补充层结果：

- target-plus-review.log（无overlay）：18顶层，16PASS/2FAIL（关闭、共享期限），另有3个index子测试。
- race-review.log（无overlay）：四个准确用例×10，40RUN，原嵌套/Task累计修复20PASS，关闭/期限20FAIL，DATA RACE=0，EXIT1。
- overlay-logic.log：6顶层，5PASS/1FAIL（新并发初始化）。原串行失败回滚、Reset超时保护通过。
- race-overlay-init.log：并发初始化复现10RUN/10FAIL、DATA RACE=0、EXIT1。

本轮所有独立用例都保留源/hash/diff：原十份从preserved复用；新增review_deadline_test.go与review_init_race_test.go。补齐Config仅用原工作区只读快照；没有修生产代码使测试通过，没有把overlay算作干净提交通过，也没有把“无DATA RACE”误作行为正确。

## 保留门禁与非阻塞项

2C负责维度六处及模型身份fingerprint、写入/检索guard；新Embedding profile生产门禁继续保留。2D在线探针须另行授权。A-4、真实ES/Qdrant/SQL取消、Stage1历史补证不扩大。协议和依赖未改，沿用independent-2b-a24cb8d/SOURCE_EVIDENCE.md官方依据及锁定Eino源码，不声称在线已验证。

命名、格式、归档样式不列阻塞。建议继续Qoder专家模式，集中修复四组问题；提示词见QODER_PROMPT.md。停止等待新SHA，不更新验收状态，不宣布整个Stage2完成。
