请使用 Qoder **专家模式**完成以下集中修复。目标基线 fbbaeb4c12ab48db68c778ac38bb64676ddcb5d1，分支 review/stage1a。先读独立报告 verification/independent-2b-fbbaeb4/REPORT.md 及 repros/；不要操作 Codex linked worktree，不清理他人文件/行尾差异，不回退已闭合项。

仅修两组 P1：
1. chat.go cleanupFail 在前台满队列时卡住。fgPipe.Copy(2) 的 cleanup child 从未消费；只关它不会关闭消费者 child 或父 pipe，fgWriter.Send 阻塞，bgWriter 无法关闭，coordDone 永不到。失败 cleanup 必须能够解除实际前台阻塞写，并以唯一、幂等所有权兼容业务提前 Close；等待 Chat 转发和后台退出。保留成功 EOF+SSE success 才保存一次，失败不保存部分回答。原提前 Close panic 已修，不要引回。
2. budget_adapter.go 的 WG 未登记执行 bsr.Recv 的临时 goroutine。Close/Done 不能在实际 Recv 尚未返回时宣告完成。父 ctx 取消在输出背压下也必须独立收敛并释放 slot，不能靠测试追加 Close 掩盖。禁止用新增未登记裸 goroutine/GC finalizer/unsafe 解决阻塞。保持受控 StreamSession 架构、真实 HTTP 取消、错误链、保守 token 结算及单消费者。

先理清锁定 Eino API 与所有任务、pipe、cancel、lease 所有权，再实现。无需恢复纯 reader.Close 驱动旧契约，也无需升级依赖或实现 2C/2D。

测试必须触达真实生产共享构造/Chat 编排：
- 长 SSE (>8 chunks)，前台停止消费后失败 cleanup，需独立有界完成，不得靠测试先关实际前台 reader 才通过；无部分保存。
- 业务提前 Close 再失败 cleanup，无 panic，重复 cleanup 幂等。
- 实际 Response.Body.Read 返回前可释放闸门：Close/Done 必须等待真实拥有的 Recv 退出，测试清理释放闸门，不能伪装完成。
- 输出背压时只取消父 ctx，等待 Done/slot 释放，断言前不能额外 Close。
- 真实百炼和 OpenAI managed Close、无预算 ABC、EOF/错误/并发终结回归。

复用独立最小复现作定位，正式测试应进入提交。不得将缺少同步、提前发信号或测试自身后续 Close 当作生产收敛证明。独立十轮背压失败为 9/10，不要改写成 10/10。

在干净提交对象验证受影响包普通/race、关键精确正则 -race -count=10 -v、串行全量、build、vet。仅用已可用 MSYS2 UCRT64 进程级环境，全部离线。记录完整 SHA、命令、实际命中、退出码及环境到新证据目录；检查依赖和测试均已提交，不以原工作区未跟踪文件证明干净目标通过。仅选择性提交本轮相关文件；不合并 main，不宣告独立验收通过。交付完整 SHA、所有权修复说明、真实测试结果与保留边界，等待独立复核。
