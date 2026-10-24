使用 Qoder 专家模式。

请按“显式受控流会话/清理句柄”方案实施Stage2B剩余流生命周期修复。固定基线db8ceede9bb0e7475a3bb5b5a82e853063da5d49，先读verification/independent-2b-db8ceed/STREAM_DESIGN_REVIEW.md及REPORT.md。此提示词交给你表示选择下述受控契约调整；不是接受GC兜底或服务器自行EOF当Close成功。

1. Provider新增最小的拥有流生命周期的接口/会话。会话提供reader及统一Close/cleanup、必要完成信号；实际关闭操作取消传给HTTP/SDK的derivedCtx、关闭自己拥有的资源、等待转发任务与lease终结。具体命名自定，不提前建大框架。明确reader/Copy分支的单一关闭责任，避免double-close。使用锁定Eino公开API，不unsafe、不反射、不擅自升级依赖。
2. 将默认Provider生产建流接到该会话，不能只新增未调用接口。Eino BaseChatModel.Stream签名可以留作兼容入口；其原始reader.Close单独无法中断网络的边界明确记录，不再伪称可实现。预算Provider的受控路径缺句柄时应显式错误，不静默绕过。旧无Provider兼容路径保留现有Stage1取消协调，不扩大历史改造。
3. Chat.GetAnswerStream/streamCoordinator持有并使用统一清理能力，业务调用方不各自散落cancel+Close。严格保持Stage1顺序：失败cancel→通知失败→关闭拥有分支→等待；成功通知成功→等待EOF消费和保存→关闭拥有分支→释放取消资源。保持成功保存一次、失败不保存部分回答、错误链及SSE帧。EOF成功结算后再清理不得误改为Abort或重复退款。
4. 移除lease==nil后台抢读原sr的goroutine。无预算与有预算均只有一个数据消费/转发所有者，正文ABC完整到达；管理cancel不能靠抢读。Pipe转发不是关闭机制本身；转发遇非EOF错误必须传给调用方，不能简单return吞错。
5. 移除finalizer作为正确性机制，GC与任何调用方引用存活都不能决定Close是否释放。不要再仅加defer、未用ctx或100ms等待。
6. 明确更新Stage2设计中的流所有权/验收边界：采用受控会话Close/cleanup，不是raw Eino reader.Close。保留原失败测试与API限制记录，不能靠删测试或改成服务3秒自行结束掩盖调整。

必要真实回归：httptest发送首条后无限等请求取消；仅调用受控Close，保留reader强引用，不额外cancel、不runtime.GC、不先关服务，即验证HTTP ctx取消/body释放、生产任务done、slot归零。百炼和OpenAI均覆盖。另覆盖无预算完整ABC、父ctx取消、EOF/读错/提前Close、并发关闭与EOF幂等、建流失败释放资源、真实Chat/SSE成功保存与失败不保存。测试经过实际生产构造/会话/协调器，不复制转发循环。

先验证最小真实关闭路径，再扩大回归。现成MSYS2 UCRT64进程级环境；普通/race、关键准确正则-race-count10-v、串行全量、build/vet，记录完整SHA/CWD/env/命中/EXIT。必要生产/测试全部选择性进入提交，使用干净对象验证，不借未提交文件计PASS。

不碰Codex审查worktree或历史证据，不连接真实DB/ES/Qdrant/模型、不改系统、不升级依赖。保留已闭合Controller owner、预算scope/deadline、初始化/Copy、opts/重定向/usage等；2C/2D门禁保持。

按既有项目提交推送授权交付新完整SHA及实际生产关闭链，不强推不合并main；不宣布独立验收通过或Stage2完成。若发现必须超出此最小接口调整范围，先给具体差异与原因，不自行扩成重写HTTP/SSE协议。
