模式选择：Qoder 专家模式。任务涉及流关闭、实例发布及预算scope所有权，使用专家模式集中修复；不分派给flash。

固定基线64aac44155414403d5d4e007863987e1f79acfe2。先读verification/independent-2b-64aac44/REPORT.md及最终日志。此次有四组阻塞；不要重做已正确的opts、重定向、usage collector、OpenAI预算和Send语义。

1. 交付完整性：提交树Config没有Registry字段，core.New无法编译。原工作区config.go和indexer.go修改未提交。不要再次以脏工作区build/PASS当目标交付。选择性补齐必要定义/接线，勿git add全部、勿混CRLF/他人文件；原未提交Index代码创建新空RequestBudget，不能直接补交宣布通过。
2. 两处Stream包装仍依赖下次writer.Send才看到下游关闭；源Recv阻塞时消费者Close不能关闭源，slot/连接/goroutine留存。真正把返回reader的Close/ctx取消传导到源、lease及可等待的任务收敛。单纯改Send判断已完成，不够。不要以测试手动srcWriter.Close来模拟生产应完成的关闭。保留多chunk、错误链、保守usage及Stage1 SSE契约，同时覆盖百炼与通用OpenAI包装。
3. 请求/任务owner：common.WithRequestBudget每次覆盖既有owner，刷新已耗尽子额度和固定deadline。最外层创建一次，嵌套入口保留并传递；创建失败显式处理。WithTaskBudget创建tb却另造rb供适配器消费，转交tb看不到同步使用。让实际适配器消费与移交的是同一累计scope，Index后台不得new空owner，也不重新获取全局Registry换scope。按既定服务root后台context/任务期限契约协调，勿把请求取消错误带入后台。添加真实Retrieve→Rewrite/Rerank→最终生成及Index同步/后台生产共享链回归，不能仅手动操作TaskBudget核心。
4. 初始化回归：core.New目前检查索引前SetRegistry(conf.Registry)，失败无回滚、旧全局被替换。通过显式实例依赖使构造使用本次Registry，成功才由持有者原子发布；失败保持原引用，处理并发代次，不能临时写全局冒充局部注入。保留Reset超时依赖和身份保护。

独立证据：不使用overlay的三项复现（阻塞SSE提前Close、嵌套owner、TaskBudget实际scope）-race-count10共30RUN/30FAIL；正式providers race及所选370次关键执行通过，但不证明上述编排。初始化失败复现仅overlay补Config字段，实际401后Registry污染。报告内原始测试副本只读可参考，不操作Codex审查worktree或覆盖证据；在自己的实现范围添加适当回归。

验证：先从Git对象确认所有必要生产/测试文件已交付，使用干净独立构建视图，不借助原工作区未提交文件。现有MSYS2 UCRT64仅进程设置；受影响包普通/race、关键准确正则-race-count10-v、go test -p1 ./... -count=1、go build ./...、go vet ./...。完整记录SHA/CWD/env/命中名/EXIT。全部离线httptest，禁止真实DB/ES/Qdrant/模型、依赖升级或系统排障。若任何必要文件仍未入对象，不宣称提交测试通过。

保持2C维度/fingerprint、新Embedding生产fail-closed及2D授权探针边界。不要修改已正确部分来迎合fixture，不新增命名/格式修复轮次，不宣布独立验收通过或Stage2完成。遵守已有提交推送授权，不强推不合并main，交付新完整SHA、四项证据、实际生产调用链及剩余边界后等待复核。
