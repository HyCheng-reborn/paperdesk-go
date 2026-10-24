使用 Qoder 专家模式。固定基线4356f6547142de470c13b7af1368f6ccaa3bd4bc，先读verification/independent-2b-4356f65/REPORT.md。Controller owner已由独立真实编排+十轮确认闭合，不重做它。现在只剩流关闭正确性和测试签名交付缺口。

请改变执行顺序：先核实可实现的关闭设计，并用原真实阻塞条件做最小离线验证；验证通过后才把方案接入生产。不要再次只添加ctx形参/defer就宣称完成。

事实：wrapLeaseStreamReader与wrapBudgetStreamReader新增ctx完全未使用。消费者仍拿pipe reader，关闭它不能唤醒正在阻塞的源Recv；defer要退出后才执行。本轮目标生产源码下，百炼与通用OpenAI真实SSE首chunk后保持连接，消费者Close后InFlight仍1，各-race-count10均10FAIL。所有数组流或测试手工srcWriter.Close都不能覆盖该条件。

1. 阅读锁定Eino v0.6.0 schema.StreamReader具体struct和公开Close API，以及SDK流HTTP body/ctx所有权。明确到底由哪个实际对象捕获业务返回reader的Close，哪个机制打断网络读取，谁等待转发退出和终结lease。不要假设定义同名Close能代理struct的方法。先写最小可执行验证：本机httptest发送首条后阻塞，读首条后只关闭业务真正拿到的reader，服务不再发数据、不自行结束、测试不预先取消或关源，等待slot/源/转发收敛；仅cleanup在失败后外部取消并等待。
2. 若锁定公开API无法满足现有契约，明确提出最小接口/适配或依赖边界调整方案供用户决定，不擅自升级依赖、不用unsafe/反射改SDK内部，也不静默把契约降成“必须额外取消ctx”。不要再交出无法触发上述原复现的伪关闭修复。
3. 可实现方案验证通过后同步用于百炼和通用OpenAI包装，保留多chunk、usage保守结算、错误链、幂等与Stage1 SSE/cleanup。用实际任务done/源关闭信号证明收敛，不能只靠lease.Finalized当作goroutine完成（finalize先于源关闭的defer可能执行）。
4. 签名改动遗漏提交内integration_test.go:327/361/392。原工作区有3行真实未提交修正，先选择性处理必要调用与回归。干净目标测试/full/vet目前失败，只有build通过。禁止用含未提交文件的工作区日志证明交付对象；提交后git show核对并用干净视图重跑。

测试必须走真实生产wrapper/HTTP构造，业务实际返回reader.Close；不要复制转发循环，不用数组、主动关上游或只cancel+关源来冒充关闭证明。原Controller共享owner与任务deadline/初始化并发独立回归应继续通过。必要测试全部进入提交，别触碰Codex审查worktree或其历史证据。

用现有MSYS2 UCRT64进程级环境，离线httptest；先跑原两个关闭条件，再受影响包普通/race、准确-race-count10-v、全量-p1、build/vet，记录完整SHA/CWD/env/CMD/命中/EXIT。不接真实DB/ES/Qdrant/模型、不改系统、不扩大2C/2D、保留新Embedding fail-closed。

遵守既有提交推送授权，选择性提交、不强推不合并，不更新独立验收通过状态。交付时先说明真实关闭机制和原复现结果，再给新完整SHA/证据/边界；若仅完成接口方案待决策，如实说明，不宣布实现完成。
