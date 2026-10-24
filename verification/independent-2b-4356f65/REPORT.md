# Stage 2B 独立增量复核：4356f654

**结论：存在阻塞，不能验收 Stage 2B。** Controller最外层owner已闭合；真实阻塞流的消费者Close仍未接线。此外，改动包装函数签名后遗漏三处提交内测试调用，干净目标的providers测试、全量和vet失败；生产build通过。

日期：2026-10-20。基线 **02d6bae0ce75f7f6207d9bf37d672dc6033285c8**；目标 **4356f6547142de470c13b7af1368f6ccaa3bd4bc**。两对象存在，基线为祖先，审查17文件完整增量及原两项问题的实际生产链。

## 目录和证据保护

复用 C:/Zhy/code/go-rag-review-1d3 detached linked worktree；common dir C:/Zhy/code/go-rag/.git。未clone/fetch，未切换或清理主工作区，未改Git配置；未发现适用AGENTS.md。切换前十三份review测试保存到preserved/并记录SHA256。历史报告未覆盖。

先完成干净目标全部正式命令，再添加复现。最后生产文件git diff --exit-code -- server为0；十份review文件为未跟踪测试，源/hash/diff在repros/，状态在final-worktree-status.txt。

本轮没有overlay、没有补改目标生产文件或提交内测试。为绕开目标百炼测试包的签名编译缺口，将原外部HTTP复现文件从bailian_test放到openai_test，仅改变包名与文件所在目录；调用的仍是同一目标生产bailian.BuildChat/BuildEmbed及providers.BuildChatRole。其他common/logic/controller原复现按原位置复用。

证据分层：干净目标日志与目标＋review日志分开；原仓库未提交integration_test.go只读保存到implementer-uncommitted/，未复制到审查包执行。未修改业务代码、方案、进度或验收状态，未提交/推送/合并。

## 阻塞1 / P1：流消费者Close仍不能唤醒阻塞源，新增ctx没有被使用

目标位置：

- server/core/providers/bailian/chat.go:253–280（wrapLeaseStreamReader）。
- server/core/providers/budget_adapter.go:378–405（wrapBudgetStreamReader）。

包装函数现在接收ctx，但函数体完全不使用它；实际仍新建pipe，goroutine阻塞源Recv后再向pipe Send，退出时defer关闭源及finalize。消费者Close的是返回pipe reader，没有直接中断源的控制链。源若没有下一条数据，goroutine无法运行到Send或defer。

本次删除旧内部Close方法并增加未用ctx参数，未改变原复现条件。即使ctx来自真实HTTP调用并可外部取消，也不等同“消费者Close已执行取消”。本问题不要求消除所有检查后取消窗口，而是显式Close后确定阻塞的源仍未中断。

**独立实际证据，目标生产源码，无overlay**：

- TestSupplementStreamEarlyCloseFinalizes：百炼真实HTTP SSE发首chunk后保持连接；消费者Recv一次再Close；100ms观察窗内InFlight=1、InputReserved=2、OutputReserved=8192。cleanup才额外取消请求、释放服务并等待handler/slot退出。
- TestIndependentOpenAIStreamEarlyCloseFinalizes：同条件经过通用providers.BuildChatRole预算包装，同样失败。
- 两项普通测试失败；-race-count10分别10/10失败。race-review.log总70RUN，50PASS、20FAIL、0DATA RACE。不是工具链异常，不是测试没执行。

影响：消费者停止读取后仍占模型连接、并发预算slot和转发goroutine，可阻挡后续请求；预算完成与资源释放不能被正确等待。

最小方向：先核实锁定Eino v0.6.0实际可实现的Close/取消接线，再实现实际返回reader的关闭→源请求/读取中断→lease保守幂等终结→转发退出。不能再以增加ctx形参、退出defer或命名为BusinessClose的测试替代生产接线。如果具体struct API限制确实需要最小接口/适配方案，应明确设计边界，不擅自改依赖或用生产unsafe/反射绕过。

**新测试为何未捕获**：

- providers/stream_close_test.go 的BusinessClose三项使用可继续读取的数组，转发者能够到达下一次Send，不能覆盖源Recv阻塞。
- CtxCancelWakesSource在cancel后主动srcWriter.Close，替生产释放源；ctx本身在wrapper未被使用。
- BusinessCloseWaitsGoroutine用lease.Finalized作为退出证明；finalize也在Recv错误/EOF内部执行，且defer finalize先于inner.Close执行，不能单凭该标志证明所有源关闭和goroutine退出。
- bailian新增BusinessClose同样使用数组。原BlockedSourceClose仍由测试主动关闭上游，条件未补齐。

测试已进入提交是进步，但上述真实性缺口应随实际关闭修复一起解决，不拆成重复阻塞。

## 阻塞2 / P1（交付/验收）：测试调用未随函数签名提交，目标必要验证失败

目标位置：server/core/providers/bailian/integration_test.go:327、361、392。

wrapLeaseStreamReader已增加context参数，但这三处仍传旧的(sr,lease)，编译器报not enough arguments。因此生产go build虽通过，百炼测试包无法编译；providers/...普通/race、关键十轮、串行全量与go vet整体失败。

只读核查原仓库integration_test.go存在真实未提交3行签名修正（增加context.Background），不是CRLF幻影。该文件的源/hash/diff在implementer-uncommitted/，本轮没有将它算入目标。实现者日志的全绿结果不能替代提交对象证据。

最小方向：选择性补交所有必要测试调用/真实回归，在干净新提交对象重新验证；不要git add全工作区。将“编译器实际测试对象”与“交付SHA”一致作为交付前门禁。本项是提交测试编译和必要验收缺口，不声称生产程序本身无法编译。

## 原问题闭合情况

| 原项 | 本轮结果 |
|---|---|
| 消费者Close接线 | 未闭合；两Provider真实阻塞SSE复现各十轮失败 |
| ChatStream最外层owner | 闭合。Controller派生ctx后WithRequestBudget，并把同一ctx传retrieve与answer；原实际Controller复现普通及十轮均通过 |
| 必要测试提交 | stream_close_test.go和Controller新测试已提交；但旧调用签名修正遗漏，仍有交付缺口 |

保留已闭合：Config.Registry/Copy、任务固定deadline及共享scope、嵌套owner、初始化显式Registry与失败/并发保护、opts输出预留、重定向、per-call usage、OpenAI预算、Send多chunk、Reset超时引用保护、显式Chat cfg、Rerank错误传播、nil输入/Client、Embedding guard/index、builder同步。原任务期限、嵌套/共享scope、初始化并发及Reset复现本轮未发现回归。

Controller提交内三个新测试允许nil==nil，CumulativeLimit也未实际消耗额度；它们不能单独证明非nil共享预算。但本轮独立测试安装真实Registry/ProcessBudget，经真实Controller与生产helper验证非nil身份共享，并十轮通过，故不因测试薄弱重新阻塞已修复的owner接线。

## 实际生产链和预算覆盖

ProviderConfig→持久ProcessBudget/buildRegistry→Registry→builder→百炼独占预算或通用budget*Role→SDK/旧Rerank HTTP，保持原scope/协议分工。ChatStream最外层现创建owner，retrieve/answer及其内层helper复用，不再各自刷新。Index同步/后台维持共享累计scope及固定期限；core.New使用显式Registry、成功后工厂发布，未恢复全局临时切换。

模型发送预算接线仍有效；未闭合的是流消费停止时的资源/slot所有权。不能把账本单元测试或数组流测试通过描述成完整业务Close保护。

## 独立验证

实际CWD C:/Zhy/code/go-rag-review-1d3/server；Go1.27.1/windows/amd64、MSYS2 UCRT64 GCC16.2.0、binutils2.47.20260726；CGO=1、GOFLAGS空。仅进程PATH/CC/CXX配置，GOPROXY/GOSUMDB=off，无系统/工具链/Git改动，未连接真实模型/DB/ES/Qdrant，HTTP全部本机httptest。

| 干净目标命令（精确参数见run-clean.ps1和各日志） | EXIT | 实际情况 |
|---|---:|---|
| 受影响包普通测试 | 1 | 214顶层PASS、239RUN含子项，但bailian测试编译失败，整体失败 |
| go test -race ./core/providers/... -count=1 -timeout=180s -v | 1 | root/openai执行54顶层PASS、68RUN含子项；bailian编译失败 |
| controller/common/rerank/logic-rag/chat race | 0 | 五包实际执行通过，181顶层PASS/192RUN，无DATA RACE |
| providers关键准确正则-race-count10-v | 1 | 可编译包130RUN/130PASS，bailian不能执行，不能称完整压测通过 |
| go test -p 1 ./... -count=1 -timeout=180s | 1 | 部分包通过，但bailian测试编译失败 |
| go vet ./... | 1 | 同一测试签名错误 |
| go build ./... | 0 | 生产构建通过 |

本轮失败不是0xc0000139环境异常。关键130PASS来自可编译子集，不是全命令通过；无DATA RACE也不代表行为正确。实现者本批50PASS命中数组/手动关源用例，既不覆盖独立阻塞条件，也不能重现于当前未补齐测试签名的目标包。

**目标＋独立测试，无overlay**：

- target-plus-review.log：21顶层，19PASS/2FAIL（百炼与通用OpenAI提前Close）。
- race-review.log：七项×10，70RUN、50PASS/20FAIL、0DATA RACE、EXIT1。Controller owner、任务期限、原嵌套/Task累计scope及初始化并发五项各10PASS；两关闭各10FAIL。

外部HTTP复现来自preserved/server_core_providers_bailian_review_fix_behaviors_test.go，运行副本放到core/providers/openai/review_fix_behaviors_test.go，仅改package为openai_test，以免编译无关的错误百炼测试文件。直接调用的仍是目标bailian及通用生产构造，不是fake实现。其余九份common/logic/controller原测试按原位复用。未重跑的三份旧百炼包测试保存在preserved/（原多chunk内部wrapper及协议/单独OpenAI预算），既往证据保留，不把未执行写成通过。本轮全部十份review源/hash/diff存repros/。

## 后续门禁和建议

2C继续负责六处维度、模型身份fingerprint及写入/检索guard；新Embedding profile生产启用保持fail-closed。2D在线探针另行授权。A-4、真实ES/Qdrant/SQL取消及Stage1历史补证不扩大。本批依赖/协议未变，沿用原官方资料记录和锁定Eino源码，不声称真实服务验证。

无命名/格式阻塞。建议Qoder专家模式先给出可实现且能通过原真实阻塞复现的最小关闭方案，再修改生产接线；不要继续将未用ctx/defer称为修复。具体提示词见QODER_PROMPT.md。停止等待下一步，不更新验收状态、不宣布Stage2完成。
