给 Qoder 专家模式的修复提示词

请基于完整 SHA 5bf62f0069a527b4940b8336f3cb953ccb551813 修复 Stage 2A 独立复核阻塞，先读：
C:/Zhy/code/go-rag/verification/independent-stage2a-5bf62f0/REPORT.md
以及同目录 reproduction-final.log、review_stage2a_test.go、reproduction.diff。

本任务涉及预算状态机、上下文生命周期与配置隔离，使用专家模式。不要扩大成 Stage 2B/2C；不调用模型 API，不连接真实设施，不重开 Stage 1，不操作或清理 Codex 审查 worktree。审查方测试只读参考，提交你自己核实后纳入正式测试的版本，不依赖原工作区未跟踪文件。

集中闭合：
1. B1：拒绝非法负预算配置；无效/不完整 usage 不得释放未知 token 预留或降低 used；所有账本运算防溢出，异常时 fail closed，同时保证 slot 释放。保留实际 usage 超过粗估时的正确扣账，不把粗估冒充精确金额上限。
2. B2：owner 的取消和绝对 deadline 不能因 Reserve、子步骤、重试刷新；ReserveCtx 同样受 owner 约束。明确 TaskBudget 后台移交，保留累计预算及固定边界，执行 context 继续遵守 Stage 1 服务 root/task timeout 和 HTTP 取消隔离。
3. B3：无歧义稳定缓存键；Registry/adapter 持有独立校验快照，不通过 Profile()/resolver 泄漏内部可变引用。相同身份仍共享，失败不发布。
4. E1：在已有可运行环境补齐受影响包及关键用例 race，普通重复不能替代。若仍 0xc0000139，如实写“运行异常、race 未完成、根因待确认”；不要自行升级工具链或反复修系统，交付时明确此证据缺口未闭合。

验证应经过生产共享内核；补齐负 usage、只有 total、不一致 usage、溢出、非法配置、owner 已取消/过期、不刷新 deadline、配置对象变更隔离、键碰撞回归，并保留原并发预留/双作用域/幂等终结/后台累计测试。计数核心与尚未接入的 HTTP/stream 生命周期分开报告，不要求在本任务接完整 2B，也不要以测试手写 Finish 编排证明生产自动终结。

提交前在干净目标运行普通受影响包、race、准确关键用例 -race -count=10 -v，以及一次串行全量/build/vet。记录实际命中测试、完整命令、CWD、环境和退出码。新的测试必须进入交付提交。保持新 Embedding profile 未在默认业务启用，直到维度/身份 guard 就绪。

交付完整 SHA、相对本基线的变更范围、逐项修复与证据、未完成边界。不宣布独立复核通过或整个 Stage 2 完成。提交/推送范围沿用项目已有明确授权；不要从本提示词自行扩大权限，不合并 main。
