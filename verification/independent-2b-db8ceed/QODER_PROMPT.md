模式：Qoder 专家模式。
任务：Stage2B流关闭的最小可行设计与离线验证。本轮先不要继续改正式生产代码或发布“修复完成”提交，不更新验收状态；请先解决API/所有权可行性，再交用户决定是否实施。

固定基线db8ceede9bb0e7475a3bb5b5a82e853063da5d49。先读verification/independent-2b-db8ceed/REPORT.md、target-plus-review.log、race-review.log及repros/；不操作Codex审查worktree/历史证据。此次干净build/vet/full/常规race均通过，但两Provider真实关闭及新增无预算抢消费各10/10失败。

一、先纠正证据结论
你新增的两项TestIndependentStreamEarlyClose并非原独立触发条件：服务器3秒后自行结束，断言给5秒，因此约3秒PASS只证明EOF收敛。原服务保持连接直到请求取消/失败cleanup释放；消费者Close后slot仍1。不要改宽等待、主动结束服务或强制GC来宣称原复现通过。

二、核实真正可实现的关闭接口
在锁定Eino v0.6.0和SDK源码上标出：谁实际拿到reader；Close能通知谁；底层HTTP读取如何被取消；lease/任务何时确认收敛。现在外层pipe reader.Close不直接调用adapter，转发goroutine阻塞源Recv，退出defer要先退出才执行。SetFinalizer只依赖不可达和GC时机，Close不等于不可达，调用方可以合法保留已关闭reader。

请给最小设计，清楚区分：
- 若不改变已批准接口/依赖即可实现，指出真实公开API及具体控制链，先在独立实验验证。
- 若锁定具体struct接口不能表达必要Close通知，坦诚列出最小接口/适配边界变更及调用方影响，供用户确认；不要用unsafe/反射改SDK，不擅自升级依赖，不把契约悄悄改成“调用方必须再取消ctx”。
不要继续堆叠未用ctx、退出defer、finalizer或定时等待。finalizer至多是漏关兜底，不能作为确定Close验收机制。

三、离线最小验证必须满足
1. 本机httptest首chunk后一直等请求ctx取消，无服务自行EOF。
2. 调用方Recv首条后，只Close业务实际拿到的reader，并runtime.KeepAlive保留其引用；不强制GC、不额外cancel。
3. 在有限观察期限内，源收到取消/关闭、slot释放、转发任务真正退出，具有真实done/源事件证明，不能仅用Finalize标志证明所有资源结束。
4. 仅失败cleanup才额外cancel/放行服务并等待所有任务退出。
5. 百炼与通用OpenAI共用可证明的机制；保留多chunk/usage/错误/SSE/既有cleanup规则。
若方案实验不通过，就如实交可行性结论和需要的最小决策，不宣称实现成功。

四、新增回归需同时设计消除
bailian/chat.go lease==nil分支启动goroutine读sr.Recv，却把同一个sr返回调用方。独立真实SSE A/B/C复现里，后台读光后调用方得到空正文，10/10失败。无预算是合法公开构造路径，不应改变流内容；不得用第二消费者“等待耗尽”管理cancel。通用层同形分支核查实际可达性，不夸大未复现路径。

五、交付
提交一份简洁方案/独立实验报告：真实控制链、锁定API证据、可行/不可行结论、最小接口影响、上述测试实际命令/命中/EXIT、下一步需用户决定什么。本轮不提交/推送正式业务修复，不宣布2B通过。不要重做已闭合Controller owner、deadline/scope、初始化/Copy/预算等；2C/2D边界保持。

全部离线，使用现成MSYS2 UCRT64进程级环境，允许临时实验/httptest；不接真实DB/ES/Qdrant/模型，不改系统或依赖。不要复制生产循环到fake里当实现证明，不篡改原关闭复现以满足当前代码。
