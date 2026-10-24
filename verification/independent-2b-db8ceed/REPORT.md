# Stage 2B 独立复核：db8ceede

**结论：存在阻塞，不能验收 Stage 2B。** 提交自带测试及build/vet/full/race均独立通过，但消费者Close契约仍未实现；新增无预算分支还会抢走调用方的流正文。三项独立行为复现均十轮失败。

日期2026-10-20。基线 **4356f6547142de470c13b7af1368f6ccaa3bd4bc**；固定目标 **db8ceede9bb0e7475a3bb5b5a82e853063da5d49**。对象存在，基线为祖先；检查17文件完整增量及原关闭/测试交付问题相邻路径。

## 工作区与证据

复用 C:/Zhy/code/go-rag-review-1d3 detached linked worktree，common dir C:/Zhy/code/go-rag/.git。未clone/fetch/切换主工作区/清理CRLF/修改Git配置；未发现适用AGENTS.md。切换前十份review测试移入preserved/并留SHA256，历史报告未覆盖。

全部正式命令先在干净目标执行，完成后才加入独立测试。最终目标生产文件git diff --exit-code -- server为0，十一份review文件未跟踪，源/hash/diff在repros/，状态见final-worktree-status.txt。

证据层：

1. 干净目标自带测试：ordinary/race-providers/race-wiring/race-key-stress/full/build/vet.log、environment.log、run-clean.ps1。
2. 目标＋独立测试，无overlay：target-plus-review.log、race-review.log。没有补改目标测试或生产代码，没有借未提交生产依赖编译。
3. 实现者日志：verification/stage2b-fix7/。实际命中与触发条件按源码核查，不以摘要名称代替证明。

未修改生产代码、方案、进度或验收状态，未提交/推送/合并。

## 阻塞1 / P1：GC finalizer不能实现确定的Close，原阻塞源复现仍失败

目标位置：server/core/providers/bailian/chat.go:130–155、277–308；server/core/providers/budget_adapter.go:84–106、397–427。

当前消费者关闭的仍是外层pipe reader。转发者在inner.Recv阻塞，无法到达Send/defer；新增streamCancel放在退出defer中，仍须先退出才能取消，没有形成唤醒路径。

SetFinalizer仅在reader不可达并被GC发现后才可能运行，**Close本身不使对象不可达，也不触发finalizer**。调用方可以合法保留已关闭reader引用；低分配/未GC时也无及时回收保证。它可以作为漏关资源的最后兜底，不能成为Close后释放连接/slot的必要条件。这里不是要求消除所有取消窗口，而是显式Close后源会确定地继续阻塞。

本地官方依据：锁定Eino v0.6.0 schema/stream.go:386–407 的Recv/Send行为；安装Go源码 C:/Program Files/Go/src/runtime/mfinal.go:341–347、370–374 说明finalizer由不可达对象的GC过程调度、时机任意。未改依赖或凭经验假设存在Close回调。实际Send返回true表示closed，摘要L1中的false描述仍写反；生产if分支本身未重现首条截断，不把注释错误另列问题。

**独立实际HTTP证据，无overlay**：

- TestSupplementStreamEarlyCloseFinalizes（百炼）与TestIndependentOpenAIStreamEarlyCloseFinalizes（通用OpenAI）。本机服务器发送首条后保持连接，直到请求取消或测试cleanup释放；不会3秒自行EOF。
- 消费者读首条后只Close实际返回reader，观察窗内InFlight=1、InputReserved=2、OutputReserved=8192。cleanup才取消外层ctx/释放服务并等待handler和slot收敛。
- 在原复现上增加defer runtime.KeepAlive(sr)，只保证关闭后的reader仍合法可达，防止GC偶然掩盖问题；没有调用GC、禁用GC或改系统。
- 普通两项失败，-race-count10各10FAIL，无DATA RACE。关闭强引用的对象也应释放源，因此该触发条件符合Close契约。

后果：停止消费后仍持模型连接、转发任务和预算slot；是否释放取决于外部取消、服务器EOF、超时或GC，无法提供既定资源生命周期保证。

最小方向：先确定锁定Eino API下可落地的显式所有权/取消接口，证明实际返回reader的Close能驱动源中断和可等待收敛，再接生产。不能继续把SetFinalizer、退出defer、增加形参或等待更久当作Close接线；若必要接口/依赖边界超出现行批准范围，应给出最小设计让用户决定，不能静默降低验收契约。

### 实现者“原复现10/10通过”为何不能闭合

目标bailian/integration_test.go:789在3秒后主动结束handler，测试:842却允许5秒才断言；openai/register_test.go:236、286相同。两项名为TestIndependentStreamEarlyClose的新增测试观察到服务自身EOF即可通过，未要求在EOF发生前Close主动断开。

实现者race-close-bailian/openai.log记录每轮约3.00秒，与自行结束时间吻合。本轮target-plus-review.log也同时出现提交内该测试3.01秒PASS，而原保持连接复现FAIL。这是不同触发条件，不能称原独立复现已经通过。没有必要怀疑日志伪造；它证明了较弱的服务器自行结束路径，而非所需Close路径。

## 阻塞2 / P1：无预算百炼Stream新增第二消费者，抢走正文

目标位置：server/core/providers/bailian/chat.go:140–152。通用budget_adapter.go:93–103有相似分支，但本轮实际可达复现针对公开bailian.BuildChat的合法Budget=nil构造，不冒称已证明通用OpenAI无预算路径也执行该分支。

lease==nil时，适配器启动goroutine持续sr.Recv，又把**同一个sr**返回调用方。后台不是只管理cancel，而是直接消费真实消息；它可抢走部分或全部内容、usage与错误，并在读到EOF后取消请求。原先无预算分支直接返回reader，本次引入了语义回归。

**独立真实HTTP复现** TestIndependentNoBudgetStreamHasOneConsumer：

- 使用公开bailian.BuildChat，合法BuildConfig未传Budget；本机SSE发送A/B/C和[DONE]。
- 调用方暂不Recv，等待服务器观察到适配器自行取消请求的确定信号；此时后台已读完。
- 调用方随后读取，实际正文为空，期望ABC，测试失败。信号等待有1秒上限，外层3秒timeout及cleanup确保退出；没有固定sleep或复制生产循环。
- 普通失败，-race-count10十次全部失败。该测试与关闭问题独立，即便有完善GC机制也不能让两个消费者共同拥有同一reader。

后果：关闭预算的有效构造路径会丢答案，不再保持Eino流所有权/单一消费者语义。不是因无预算配置必须被禁止；当前API明确允许nil预算并走此路径。

最小方向：移除抢读式cancel管理，保持调用方唯一消费所有权；取消句柄应通过实际生命周期接口管理，不能用后台抢读来“等待流耗尽”。只关闭或启用预算不应改变返回内容。通用层相似代码也应按实际可达路径核实，但不重复列同一根因。

## 原问题状态及保留闭合项

| 项目 | 本轮结论 |
|---|---|
| 真实消费者Close | 未闭合；两Provider原保持连接复现各十轮失败 |
| 上轮测试签名遗漏 | 已补齐；干净providers测试/full/vet通过，闭合 |
| Controller最外层owner | 保留闭合；原实际Controller复现和十轮继续通过 |
| 新增无预算抢消费 | 本轮实际回归，需修复 |

Config.Registry/Copy、任务固定deadline及共享scope、嵌套owner、初始化显式Registry及失败/并发保护、opts输出预留、重定向、per-call usage、OpenAI预算、Send多chunk、Reset超时引用保护、显式Chat cfg、Rerank错误传播、nil输入/Client、Embedding guard/index和builder同步均保留既有闭合，不因本轮扩大复核。

实际预算装配仍为ProviderConfig→持久ProcessBudget/buildRegistry→Registry→角色builder/预算适配→SDK或旧Rerank HTTP。Controller共享最外层owner、Index共享任务scope与绝对期限继续生效；本轮故障集中在流读者/取消的所有权设计，不能以账本核心通过替代。

## 独立验证结果

CWD C:/Zhy/code/go-rag-review-1d3/server。Go1.27.1/windows/amd64，MSYS2 UCRT64 GCC16.2.0、binutils2.47.20260726，CGO=1、GOFLAGS空；仅进程PATH/CC/CXX设置，GOPROXY/GOSUMDB=off。未安装升级工具链、改系统/Git配置；未连接真实DB/ES/Qdrant/模型，HTTP仅本机httptest。

| 干净目标命令（全参数见日志/run-clean.ps1） | EXIT | 实际结果 |
|---|---:|---|
| 受影响包普通测试 | 0 | 277顶层PASS、302RUN含子测试 |
| go test -race ./core/providers/... -count=1 -timeout=180s -v | 0 | 三包，117顶层PASS、131RUN含子测试 |
| controller/common/rerank/logic-rag/chat race | 0 | 五包，181顶层PASS、192RUN含子测试 |
| providers关键准确正则-race-count10-v | 0 | 370RUN/370顶层PASS |
| go test -p 1 ./... -count=1 -timeout=180s | 0 | 串行全量通过 |
| go vet ./... | 0 | 通过 |
| go build ./... | 0 | 通过 |

正式race无DATA RACE，确实执行，不是环境异常。关键370次来自37个包限定用例×10（裸名称36个，RetryNoRefresh跨包重名），范围不自动包含原独立复现。命令与完整正则/SHA/CWD/EXIT均保留。提交测试全绿是事实，不能据此排除触发条件未覆盖的正确性问题。

**目标＋独立测试，无overlay**：

- target-plus-review.log：23顶层，20PASS/3FAIL；其中包含提交已有openai.TestIndependentStreamEarlyClose在3.01秒PASS；三个失败为两Provider真实关闭与新无预算抢消费。
- race-review.log：八项×10，80RUN/50PASS/30FAIL、0DATA RACE、EXIT1。Controller owner、任务期限、嵌套/共享scope、初始化并发五项各10PASS；上述三项各10FAIL。

十份历史review从preserved复用。外部HTTP测试仍位于openai_test，直接调用目标bailian/通用构造；仅在关闭测试添加KeepAlive来排除GC干扰。新增review_no_budget_test.go。十一份实际运行源/hash/diff在repros/，不算提交自带测试。原版源仍保留，可对照修改。

## 后续门禁和建议

2C保留六处维度、模型身份fingerprint及写入/检索guard，新Embedding profile生产仍fail-closed；2D在线探针另行授权。A-4、真实ES/Qdrant/SQL取消及Stage1历史补证不扩大。依赖及协议未改变，官方资料沿用既有SOURCE_EVIDENCE；本轮新增判断以锁定Eino和本地Go官方源码为依据。

无命名、一般文档或归档格式阻塞。下一步应让Qoder专家模式先提交可落地的关闭所有权最小方案/验证，而非继续堆叠GC兜底后宣称完成；新增抢消费必须消除。具体提示词见QODER_PROMPT.md。停止等待安排，不更新验收状态，不宣布Stage2完成。
