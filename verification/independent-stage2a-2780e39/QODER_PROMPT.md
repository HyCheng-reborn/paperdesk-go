请交给 Qoder 专家模式：

基于完整 SHA 2780e398f2a3091f60c0d6c34ce7661e1716244d，修复本次独立复核的两个剩余阻塞。先读：
C:/Zhy/code/go-rag/verification/independent-stage2a-2780e39/REPORT.md
同目录 reproduction.log、review_remaining_test.go，以及旧复现 API 适配版 review_previous_test.go。

仅集中处理：
1. 预留阶段仍溢出：无限 token 配置下，两个 MaxInt64 合法 estimate 可使 InputReserved=-2，第二个 Reserve 返回成功。canReserve 必须检查本次增量后的可表示性，不能只检查已有 used+reserved；对两个作用域先完整受检再整体应用。补输入/输出及跨作用域零残留回归，不用负值回绕或静默截断掩盖。保留已修复的结算溢出/负 usage/非法配置/slot 释放行为。
2. resolver 出口仍泄漏内部 Profile：Registry 在回调前算 key，r.creds(ctx,p) 却把同一内部对象传出，回调改 Model 后会在旧 key 下构建新模型。传入深拷贝或只读视图，让内部身份始终稳定；补回调修改字段/map、保留回调参数后修改、不影响缓存及已构建对象的测试。

B2 原始复现和本次服务 root owner 补证已通过，不重做接口框架。2B 固定 NewTaskBudget(serviceRoot) + 同步 ReserveCtx(requestCtx) + 后台 ReserveCtx(taskCtx)，不要把 owner 绑定 HTTP 请求再指望移交脱离取消。完整 HTTP/stream 预算接线仍属 2B，不在本次扩张。

不要修改/清理 Codex 审查 worktree；其中两份 review 测试和历史证据要保留。只读参考复现，正式测试经核实后写入你自己的交付提交，不依赖未跟踪文件。不调用模型 API、不连接真实设施、不升级依赖、不操作索引或数据，不重开 Stage 1。

在干净交付提交中运行受影响包普通测试、包级 race、准确关键用例 -race -count=10 -v、一次串行全量/build/vet。使用已有可运行环境；若仍 0xc0000139，记录“运行异常、race 未完成、根因待确认”，保持证据缺口未闭合，不以普通重复替代，不自行升级工具链或反复修系统。

交付完整 SHA、逐项结果、命中名称和真实退出码、未完成边界。不要宣称独立复核已通过或 Stage 2 完成。提交/推送权限按既有用户明确授权，不合并 main。
