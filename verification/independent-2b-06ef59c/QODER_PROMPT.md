使用 Qoder 专家模式。当前基线06ef59c68000059f29302710ea15ce93afdc88bd，先读verification/independent-2b-06ef59c/REPORT.md。此次仍是4组生命周期/预算/交付阻塞，不能交给flash，也不要再次只改注释或增加退出defer。

1. Config.Registry仍没进入提交，core.New在干净对象不能编译。主工作区config.go真实未提交+5行，不是CRLF。选择性纳入正确生产定义/Copy等必要传递；提交后必须用git show <新完整SHA>:server/core/config/config.go核对字段并在干净对象build。禁止再以脏工作区PASS当交付；不要git add全部、混他人文件。
2. 流Close仍失败：defer只在goroutine退出后执行，不能唤醒inner.Recv。真正实现消费者Close/ctx取消直接打断源请求/读取、终结lease并等待转发退出，覆盖百炼和通用包装。不能只期待源以后再发chunk触发writer.Send。原httptest首chunk后挂起、消费者Close的复现仍10/10失败；回归不能手工srcWriter.Close来帮助生产收敛。保留Send语义、多chunk、错误传播、保守usage和Stage1 SSE。
3. 共享Task视图丢期限：NewSharedRequestBudget必须继承tb的固定绝对deadline，不因opts重新now+duration，也不因无opts变无限。服务root后台ctx和任务绝对预算边界分开，保留已修复共享scope。原嵌套owner和Task累计复现均已通过，不必推翻重做。同时把整个ChatStream最外层owner真正共享给retrieve与answer两个兄弟阶段（局部ctx赋值不会回传controller）；构造/移交失败需可识别传播而非fmt.Printf后fallback。通过真实入口/共享生产编排验收。
4. 初始化仍临时写全局；失败无条件恢复old会覆盖期间newer。不要再用临时全局切换模拟显式Registry注入。局部构造通过显式实例依赖使用新Registry，成功才按代次/身份由持有者发布，失败不改旧引用。新增复现在httptest初始化等待点观察到半成品并发布newer，然后401失败，回滚覆盖newer：10/10失败。单次失败恢复旧值现已通过，不等于并发安全。

优先验证这四组真实触发条件再广跑测试。保留已经通过的opts输出同源、OpenAI预算、重定向、usage collector、Index累计scope、Reset超时引用保护/身份校验、显式Chat cfg、Rerank错误传播、nil输入/Client、Embedding guard/index和builder同步。勿扩大2C/2D或做命名格式重构。

新增必要回归必须进入提交、经过真实共享链，所有goroutine退出有确定信号、有限等待和cleanup。不要用无DATA RACE、空fixture/手工关源/核心预算单测替代生产行为。

用现有MSYS2 UCRT64进程级环境，先以干净对象重跑原独立条件和新增修复回归，受影响包普通/race、准确-race-count10-v、串行全量、build、vet。日志记录完整SHA/CWD/env/完整命令/命中/EXIT，不借原工作区未跟踪文件。全部离线本机httptest，不接真实DB/ES/Qdrant/模型，不升级依赖或系统。

不要操作Codex审查worktree或覆盖其历史复现。遵守现有提交/推送授权，不强推不合并main，不更新独立验收通过状态；交付完整新SHA、四组证据、实际生产链与2C/2D保留边界，等待复核。
