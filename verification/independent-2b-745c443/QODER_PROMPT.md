使用 Qoder 专家模式。本轮是 Stage2B 复杂生产接线/预算/生命周期修复，不交给flash。

固定基线745c4433b4a99542c441b7a3d6fe958f88688caa。先读verification/independent-2b-745c443/REPORT.md及证据。不要操作Codex审查worktree、覆盖历史复现或混入他人文件，不扩大到2C/2D，不调用真实模型/DB/向量库。

集中修复：
1. 提交完整性仍未闭合：RequestBudgetFromContext、ContextWithRequestBudget、ErrRedirectProhibited及profileChat缺失。原工作区未提交budget/transport/registry_test不是提交证据。选择性补齐必要生产定义/测试，不用fake TestMain掩盖实际构造。先以干净对象验证编译。
2. 锁定Eino v0.6.0 Send返回closed，当前chat.go260取反导致三条输入只输出一条。按API修正，同时把下游Close/取消到源关闭、lease终结和goroutine收敛落实，不能只翻转条件后留下阻塞Recv。保留流消息/错误/usage，不用EOF测试冒充context取消。
3. 输出预留与实际opts/default max_tokens同源；固定8192仍可WithMaxTokens(16384)绕过。真实HTTP验证拒绝或约束，不能只比较常量。
4. OpenAI builder现在直接返回无预算基础适配器；真实MaxRequests=1仍两次发送且账本0。所有支持Provider必须走共享预算链，含Chat/Embedding/Rerank。
5. 真正接请求/任务入口owner：当前只读取ctx，生产无注入；ChatStream子步骤共享，Index后台显式移交，进程预算不可随Registry重建刷新。新Registry构造上下文显式传给core.New，不能仍读旧全局再发布。
6. 实际发送和重发策略覆盖默认/注入Client。当前未提交transport只禁止默认Client重定向，注入本机Client仍两次发送记一次。协议/transport层执行真实尝试门禁，不重复扣费，也不越过预算。
7. Embedding usage不得放缓存角色lastUsage共享字段；Mutex不解决并发串账。改为调用/HTTP/lease关联，确定交错双请求分别验证owner及总账。

保留已修复：Reset超时Registry保留及身份校验、显式Chat cfg、Rerank构造错误传播、nil客户端Transport兼容、nil文档返回错误、Embedding原始index校验、生产新Embedding fail closed、builder同步冻结。

增加真实共享链回归：多chunk完整性、阻塞网络源Close/取消/错误及关闭并发、HTTP输出上限、OpenAI预算、入口owner复用/后台移交、注入Client本机307计数、不同usage双请求归属。勿复制预算实现、用fake代替真实builder或仅测试fixture。httptest全部本机；已有重定向测试使用外部域名应改为本机目标，避免门禁失效时意外联网。

用现有MSYS2 UCRT64进程级环境，在干净完整新提交运行受影响包普通/race、准确关键正则-race-count10-v及串行全量/build/vet。记录完整SHA/CWD/env/命令/实际命中/EXIT，不用脏工作区PASS替代交付。必要生产与测试文件选择性提交，遵守既有提交推送授权，不强推不合并；不宣布独立验收通过或更新通过状态。

交付新完整SHA、逐项证据、真实生产调用链和剩余边界，等待独立复核。
