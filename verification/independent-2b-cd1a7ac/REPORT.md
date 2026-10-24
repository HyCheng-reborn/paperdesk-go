# Stage 2B 受控会话独立复核：cd1a7acf

**结论：存在阻塞，不能验收 Stage 2B。** 已按选定的新契约，以StreamSession.Close验收；百炼/OpenAI真实HTTP受控关闭和无预算ABC均通过，旧GC/抢读根因已消除。但生产Chat会话路径出现重复关闭panic，通用会话完成信号没有覆盖全部任务，且提交内五处测试调用未更新。

日期2026-10-20。基线 **db8ceede9bb0e7475a3bb5b5a82e853063da5d49**；目标 **cd1a7acf9b2a381dc12471aa45ac9dff50475447**。对象存在、基线为祖先，审查完整18文件增量与实际Chat接线。

## 工作区与证据保护

复用C:/Zhy/code/go-rag-review-1d3 detached linked worktree，common dir C:/Zhy/code/go-rag/.git。未clone/fetch/切换主仓/清理CRLF/改Git配置；未发现适用AGENTS.md。切换前十一份原复现移入preserved/并记SHA256，历史报告不覆盖。

先在干净目标执行正式验证；之后新增两份独立测试。最终生产文件git diff --exit-code -- server为0，源/hash/diff在repros/，状态在final-worktree-status.txt。未修改生产代码、方案、进度或验收状态，未提交/推送/合并。

本轮无overlay。外部会话测试放在可编译的openai_test中，直接调用目标providers.BuildChatRole和bailian.BuildChat；Chat测试经过真实Chat.GetAnswerStream/setupSessionCoordinator，使用目标既有fakeMessageSaver隔离History数据库。没有修目标测试使其通过。

## P1：Chat受控会话与Copy分支的关闭所有权冲突，提前关闭前台后cleanup会panic

位置：server/internal/logic/chat/chat.go:237–269、375–428；server/core/providers/stream_session.go:70–90。

setupSessionCoordinator把session.Reader做Copy(2)，但session路径cleanupFail/cleanupOK没有像旧路径那样关闭前台分支，而直接Drain/Close原始session reader。后台defer仍关闭Copy分支。Eino父reader在两个分支都关闭时，会再次关闭原始reader；StreamSession的sync.Once只保护session.Close调用，不保护Eino Copy父对象的另一路关闭。

**实际生产复现** TestReviewSessionEarlyFrontendClose：真实HTTP Provider会话→真实Chat.GetAnswerStream→读首条→前台reader.Close→cleanup失败。后台关闭另一分支时调用parentStreamReader.close，再次关闭共享源，出现 `panic: close of closed channel`，栈在setupSessionCoordinator.func1/chat.go:428与Eino stream.go:770/422。见chat-early-close.log，单独测试进程EXIT1。此为未recover的生产goroutine panic，不只是测试断言；在服务进程中可导致崩溃。

关联证据 TestReviewManagedChatCleanupOwnsFrontend：正常EOF+成功cleanup、读首条+失败cleanup两种实际Chat路径，cleanup返回后前台分支仍未关闭，Recv返回EOF而非锁定Eino的ErrRecvAfterClosed；普通和race十轮都失败。成功保存1次/失败保存0次的断言通过，但不能据此忽略分支所有权。

最小方向：给session源reader与Copy两个分支明确唯一关闭责任；cleanup必须回收其拥有的前台、后台分支，且与session.Close不会重复关闭同一源。利用锁定API提供的适当幂等能力或统一fork管理，但不能仅补一行前台Close后仍让session和Copy父对象分别close同一channel。保留Stage1成功EOF/保存后清理、失败取消/通知/关闭/等待的顺序与部分回答不保存规则。

测试必须经过StreamSessioner模型的真实Chat路径；现有Chat/Controller老测试模型多为只实现Stream的Fake，仍走旧协调器，八包race通过并不等于新分支被覆盖。

## P1：通用会话Done仅等待转发者，未等待接收者；父取消在背压下不收敛

位置：server/core/providers/budget_adapter.go:431–505；server/core/providers/stream_session.go:70–90。

新代码创建recv与forward两个goroutine，done只在forward退出时关闭；recv既没有自己的完成信号，也没有被join。Session.Close取消后可令forward直接退出、finalize并关闭done，但recv仍在阻塞inner.Recv。把阻塞调用移到额外goroutine，没有建立完整任务收敛边界。

**实际HTTP完成边界复现** TestReviewManagedCloseWaitsReceiver：经过真实OpenAI构造与HTTP；测试transport只在实际HTTP读取已因取消返回错误后，以有限事件闸门延迟将错误交还SDK。此时SDK尚不能发布error/EOF，provider自己的recv必然仍阻塞。Session.Close和Done却已经返回/关闭。释放闸门后读取方才结束。普通失败，race十轮全部失败。此测试不要求自定义Read立即可中断；它验证当读取尚未完成时，API不得宣称本批接收任务已收敛。

**实际父取消/背压复现** TestReviewManagedParentCancelUnderBackpressure：真实服务器发送多个chunk后等请求取消；消费者不读，用只读反射观察锁定Eino返回pipe的缓冲已填满（不修改SDK内部）。取消父ctx，服务器已观察到HTTP取消，但会话Done仍不关闭，InFlight=1、InputReserved=1、OutputReserved=8192，直到额外session.Close才收敛。race十轮全部失败。

原因：cancelSig仅由session自己的cancel函数关闭，未与父ctx取消联动；forward可能堵在writer.Send，recv又可能堵在dataCh发送，无法继续观察底层取消错误。注释“buffer=1，永不阻塞”不符合channel行为。

影响：Close/Done可早于本批接收任务结束，或父请求取消后继续占slot及goroutine，破坏关闭/Shutdown依赖收敛判断。没有DATA RACE不排除这些逻辑问题。

最小方向：显式跟踪并等待本批所有recv/forward任务，不能以仅forward done代表整会话；父ctx取消和显式Close共用完整取消路径，能够解除有背压的发送并确保源可收敛。错误/EOF/取消结算由唯一终结者决定，不让多个goroutine竞争以不同原因finalize。保持实际SDK取消能力边界，不要求消除不现实的所有取消窗口。

## P1（交付/验收）：五处测试仍把StreamSession当reader，干净目标无法完成必要测试

位置：server/core/providers/bailian/integration_test.go:330、365、396；stage2b_stream_test.go:206、247。

wrapper返回已改为*providers.StreamSession，五处仍调用wrapped.Recv，目标报 `wrapped.Recv undefined`。go build生产代码通过，但providers/...普通/race、关键十轮、全量与vet整体失败。原仓库两测试文件有真实未提交修正（不是CRLF），源/hash/diff只读保存在implementer-uncommitted/，没有用于本轮编译。

最小方向：补齐并选择性提交所有必要测试更新，使用Reader/Close/Done的真实会话契约；先核对Git对象，再在干净提交视图跑验收，不能继续用主工作区含未提交内容的日志证明交付。

不拆五个独立问题，不把它当命名或文档问题。它是确定的提交内测试编译失败及必要验证缺口。

## 已闭合和真实生产接线

- **关闭机制方向有效**：无GC/finalizer；session持有派生取消能力。TestReviewManagedRealClose的百炼、OpenAI两子例均在真实首chunk后持续等待的HTTP服务上，只调用session.Close即观察到服务取消、slot归零和Done关闭，普通和十轮race均通过。说明原“无法检测raw reader.Close”问题在已批准的受控接口边界得到解决；不再以旧raw reader.Close单独中断HTTP作为验收条件。
- **无预算正文完整**：TestReviewManagedNoBudgetABC走公开bailian.BuildChat Budget=nil→StreamSession，ABC完整到达，普通和十轮race通过；旧后台抢读已删除。
- **生产Chat确实接会话**：GetAnswerStream类型断言StreamSessioner并调用StreamSession，进入setupSessionCoordinator；不是未接线的孤立接口。但该生产分支存在第一项新回归。
- 保留Controller owner、预算scope/deadline、初始化/Copy/Config.Registry、opts/重定向/usage、OpenAI预算、Index共享累计、Reset身份/超时保护、显式Chat cfg、Rerank错误、nil边界、Embedding guard/index及builder同步既有闭合，不扩大重审。

StreamSessioner在兼容层是可选接口，目前默认预算Provider和百炼均实现，普通旧模型仍走Stage1旧路径。后续新增预算Provider不能用不具受控清理的类型静默绕过；本轮不因未来模型假设另列阻塞。

## 新增提交内测试真实性

stream_session_test.go:278的TestStreamSession_CloseInterruptsHTTP创建了httptest.Server，但从未向其URL发请求，模型实际是FakeChat＋Pipe，bodyClosed也未断言。因此日志称“真实关闭路径10/10”并不能证明HTTP中断。本轮另写实际Provider/HTTP测试补足，且实际关闭正向通过，所以不为此再单独增列阻塞。

9项session单测在可编译root包中可执行，但多数用Fake/Pipe；WithInnerSessionCascade的fake自带转发不能证明默认生产Chat的分支清理。完成信号必须覆盖生产真正启动的任务，不能仅以lease.Finalized或一个forward done替代。无新增提交内Chat session集成测试，现有源码审查和独立复现暴露上述panic。

## 独立测试记录

CWD C:/Zhy/code/go-rag-review-1d3/server。Go1.27.1/windows/amd64；MSYS2 UCRT64 GCC16.2.0、binutils2.47.20260726；CGO=1，GOFLAGS空。仅进程PATH/CC/CXX设置，GOPROXY/GOSUMDB=off。未安装/升级工具链、改系统或Git配置，无真实DB/ES/Qdrant/模型调用，HTTP全部本机httptest。

| 干净目标命令（完整参数/正则见run-clean.ps1和日志） | EXIT | 结果 |
|---|---:|---|
| 受影响包普通测试 | 1 | 可编译部分225顶层PASS/250RUN含子测试，bailian测试编译失败 |
| go test -race ./core/providers/... -count=1 -timeout=180s -v | 1 | 可编译root/openai共65顶层PASS/79RUN，bailian编译失败 |
| controller/common/rerank/logic-rag/chat race | 0 | 五包181顶层PASS/192RUN，无DATA RACE；不代表新Chat session分支覆盖 |
| providers关键正则含TestStreamSession_ -race-count10-v | 1 | 可编译部分220RUN/220顶层PASS，含9项session×10；bailian未运行 |
| go test -p 1 ./... -count=1 -timeout=180s | 1 | bailian测试编译失败 |
| go vet ./... | 1 | 同一测试编译错误 |
| go build ./... | 0 | 生产构建通过 |

不是旧Windows race启动异常；部分通过不能作为全命令通过。实现者race-session-stress.log只有汇总文字，测试名存在，本轮root关键命令确实独立运行了对应用例；仍不能由汇总扩大到真实HTTP/生产Chat验收。

**目标＋独立测试，无overlay**：

- managed-review.log：最初4顶层（RealClose、NoBudget、ReceiverWait、ChatCleanup），2PASS/2FAIL；8RUN含四个子测试。HTTP两Provider正向通过，Chat成功保存一次/失败不保存的断言也通过，但前台分支关闭断言失败。
- race-managed-review.log：添加ParentCancelUnderBackpressure后5顶层×10，20顶层PASS/30顶层FAIL；90RUN包含40个子测试，DATA RACE=0，EXIT1。失败三项为ReceiverWait、ParentCancel、ChatCleanup，各10轮；正向RealClose（两Provider子例）和NoBudget各10轮通过。
- chat-early-close.log：单独测试进程运行TestReviewSessionEarlyFrontendClose，生产后台panic导致EXIT1，无正常顶层PASS/FAIL完成行；不把它称十轮压测，也不让panic中断其他证据。

两份独立源review_managed_session_test.go分别位于providers/openai与internal/logic/chat，均副本/hash/diff在repros/。接收完成闸门属于注入HTTP客户端测试；Copy状态/背压只做只读观察，未修改生产或SDK。原十一份raw/旧复现保留于preserved/；旧raw Close契约不再拿来阻塞本次受控接口方案。

## 边界与结论

2C维度六处、索引fingerprint/模型身份guard仍待实施，新Embedding生产启用保持fail-closed；2D在线探针单独授权。A-4、真实向量库/ES启动、SQL取消与Stage1历史补证不扩大。依赖和协议未改，本轮以锁定Eino源码/实际生产调用链和离线HTTP验证为依据，不声称在线服务验证。

命名、格式和一般文档不列阻塞。建议Qoder专家模式集中修复分支唯一关闭、完整会话任务收敛及必要测试交付，保留已证明有效的受控会话方向；提示词见QODER_PROMPT.md。完成报告后停止，不更新验收状态、不宣布Stage2完成。
