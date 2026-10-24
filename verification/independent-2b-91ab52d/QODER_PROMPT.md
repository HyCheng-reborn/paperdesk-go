请用 Qoder **专家模式**集中修复 Stage 2B 剩余一项 P1。基线固定 91ab52dd3f942da00fadcdbb8963a5537a0e4c9a；先读 verification/independent-2b-91ab52d/REPORT.md 与 repros/。不要操作 Codex linked worktree、清理他人文件/行尾差异或回退已闭合项。

已闭合，不重做：Chat 前台满队列失败 cleanup；提前 Close 后 cleanup 无 panic；通用包装实际 Recv 纳入 WG；OpenAI 父取消背压；百炼/OpenAI 显式 managed Close；无预算 ABC。

唯一剩余：百炼独立 wrapLeaseStreamReader 丢弃传入 ctx，在 writer.Send 背压时仅父取消不能触发输出 pipe 关闭/终结。Registry 直接返回 bailian builder role，本轮 budget_adapter 的 watcher 不覆盖它。真实 Registry→百炼 builder→锁定 SDK→httptest SSE，父取消后 HTTP 已退出，但一秒后 Done 未关、InFlight=1；显式 Close 后才释放。独立 race 十轮 10 FAIL。不是 raw reader.Close 契约问题，也不是 2C/2D。

请让百炼父取消进入确定、幂等的会话终结路径，解除输出 Send 背压，释放 slot 并收敛实际拥有任务。可以共享生命周期实现，保持接口规模适当；禁止重复预算预留/扣取/结算，禁止 GC 作为正确性机制、unsafe/反射改 SDK、未跟踪裸 goroutine 隐藏阻塞。保留消息错误链、取消/deadline、usage 保守结算及无预算正文完整性。

正式测试应经真实 Registry/shared builder + 锁定 SDK + httptest，分别覆盖百炼与 OpenAI：
- 输出满且不消费，只 cancel 父 ctx，不追加 Close/Drain 帮助断言；等待 Done，确认 slot=0、HTTP handler 与拥有任务退出。
- 有预算及允许的无预算构造路径。
- 显式 Close、EOF、读错、并发终结、提前 Close、无预算 ABC、Chat 失败背压不保存部分回答。
- 闸门和源关闭使用 cleanup 兜底；失败时也不遗留测试任务。

不要仅用 fake 的 ctx watcher 证明生产适配成立，也不要只测 providers 根包后声称覆盖 provider 子包。可参考本轮独立复现，正式测试要进入提交。

在干净提交对象离线验证受影响普通/race，准确正则 -race -count=10 -v，串行全量、build、vet。用现有 MSYS2 UCRT64 进程级环境，不升级依赖、不改系统、不连真实设施或付费模型。新证据目录记录完整 SHA/CWD/命令/环境/实际命中/退出码；选择性提交本轮文件，确保目标内无遗漏依赖。交付完整 SHA、实际调用链与结果，不合并 main、不宣布独立验收通过或整个 Stage 2 完成，等待复核。
