使用 Qoder 专家模式。固定基线cd1a7acf9b2a381dc12471aa45ac9dff50475447。先读verification/independent-2b-cd1a7ac/REPORT.md及managed-review/race-managed-review/chat-early-close.log。

受控会话方向已验证有效：真实百炼/OpenAI session.Close均取消HTTP并释放slot，无预算ABC完整，普通及十轮通过。不要推翻方案、退回finalizer，也不要继续用旧raw reader.Close单独保证中断HTTP作为目标。此次集中修复三组实际问题。

1. 生产Chat session分支关闭所有权冲突。
setupSessionCoordinator把session.Reader.Copy(2)，cleanup却不关前台分支而直接session.Drain/Close源；后台分支关闭后，Copy父对象还会关源。真实GetAnswerStream读首条、前台Close、失败cleanup触发后台panic close of closed channel。正常/失败cleanup后前台也仍开着。
请统一源reader与Copy分支的关闭责任，确保前后台分支由协调器回收，session与Copy父对象不能独立重复close同一channel。不能仅recover panic、删早关测试或干脆漏关前台。保持Stage1失败取消→通知→关闭拥有分支→等待，以及成功EOF消费/保存后再清理的规则。若采用Eino公开的幂等能力，先核实锁定API并保证所有路径按同一所有权使用。

2. 通用会话Done未覆盖接收任务，父取消遇背压仍卡slot。
wrapBudgetStreamReader新建recv/forward两个goroutine，但Done只等forward；Close可在recv仍阻塞时返回。独立真实HTTP读取完成闸门证明这一点。另一个真实HTTP测试：输出buffer已满、消费者不读，取消父ctx，HTTP已取消但session不Done、InFlight仍1，额外Close才退出。两项各十轮失败。
请跟踪并join本批所有任务；统一显式Close、父ctx取消、源错误/EOF的终结，能解除背压发送并等待接收退出。不要以新裸goroutine隐藏阻塞，不能用buffer=1宣称永不阻塞；不能以Done提前关闭掩盖任务尚活着。结算终结者和终态要唯一，保守退款规则保持。并发取消信号应用真正幂等保护，不让新的父取消监视者与Close竞争close(channel)。

3. 提交内五处测试未更新。
bailian/integration_test.go:330/365/396、stage2b_stream_test.go:206/247仍wrapped.Recv，但wrapper已返回StreamSession。主仓修改尚未入目标。选择性补齐正确Reader/Close/Done调用及必要真实回归；不要git add全部混入他人/CRLF。干净对象build虽过，providers测试/full/vet当前仍失败。

必要回归直接走生产：
- 真实Provider session→真实Chat.GetAnswerStream，新session路径成功EOF保存一次、失败不保存、前台提前Close再cleanup不panic、重复cleanup不多关、前后台分支确实回收。
- Close等待recv及forward，父ctx取消+输出背压也收敛；真实HTTP取消/受控读取完成，不用lease.Finalized代替所有任务退出。
- 保留已通过的百炼/OpenAI真实受控Close、无预算ABC和既有budget/context/owner测试。
提交TestStreamSession_CloseInterruptsHTTP当前只创建服务器却没有请求它，实际用FakeChat；应改为真实生产HTTP构造并断言源取消/退出，不能只改测试名。fake可用于局部单测，但不得复制转发循环证明生产。

用现有MSYS2 UCRT64仅进程环境：先跑上述失败条件和真正集成回归，再受影响包普通/race、准确关键-race-count10-v、串行full/build/vet。必要源码/测试全部进入Git对象，在干净视图验证，记录完整SHA/CWD/env/CMD/命中/EXIT。遇panic单独进程记录，不把进程被panic中断称十轮完成。禁止真实模型/DB/ES/Qdrant，不升级依赖/系统。

不操作Codex审查worktree/历史证据，不重做已闭合Controller owner、deadline/scope、初始化/Copy、opts/重定向/usage等；2C/2D门禁保留。按既有提交推送授权选择性提交、不强推不合并，不更新独立验收通过状态，交付新完整SHA和三组真实证据后等待复核。
