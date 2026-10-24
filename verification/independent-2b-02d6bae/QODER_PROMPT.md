选择 Qoder 专家模式。基线固定02d6bae0ce75f7f6207d9bf37d672dc6033285c8。先读verification/independent-2b-02d6bae/REPORT.md。本轮只剩两个实际生产接线阻塞及一项必要测试交付缺口，不重做已闭合部分。

已闭合：Config.Registry/Copy、干净build/vet/full、任务固定deadline、同步/后台共享scope、嵌套owner、初始化显式Registry/失败与并发回滚。独立普通和race已验证，不要推翻重做。

1. 流消费者Close仍没有接线。实际返回的是schema.Pipe的*schema.StreamReader，业务Close不会调用你新增的leaseStreamReader.Close/budgetStreamReader.Close。done只表示goroutine退出，不能促使阻塞inner.Recv退出。百炼与通用OpenAI真实SSE首chunk后保持连接的两个独立复现，各-race-count10均10FAIL：关闭后InFlight仍1。
先核实锁定Eino v0.6.0公开API，给出实际可实现的返回流Close/ctx取消→底层源中断→lease保守幂等终结→转发任务退出方案，再编码。不要继续加没有调用方的同名Close、退出defer、100ms等待或手工关上游测试。禁止生产unsafe/反射改SDK内部；若确需改变依赖或公开清理契约而超出既定范围，先给最小具体接口方案，不擅自升级。保留Generate/Stream角色和Stage1 SSE/cleanup契约、多chunk与usage/错误语义。回归必须直接关闭业务实际拿到的reader，服务不自行发下一条、不替生产先关闭源，cleanup仅在失败后外部取消并等待。

2. 整个ChatStream最外层owner未接线。controller只WithCancel，分别向retrieve和answer传无预算ctx；Retrieve局部创建owner不会回传controller，回答又造一个。WithRequestBudget的“已有则复用”现在正确，但两个兄弟阶段没有共同父owner。请在实际最外层创建一次并传给两阶段，保持取消、deadline、错误传播与旧无Provider分支。真实controller实例注入边界+同一生产helper复现目前10/10失败。测试不仅手工给适配器ctx注入rb，要验证实际编排的owner身份及累计限额。

3. server/core/providers/stream_close_test.go仍未提交，交付130次race包括此文件5项，目标树没有。核实归属、修正测试真实性后选择性补交必要测试；部分测试复制转发循环/直接内部Close，不足以证明生产包装。不能仅git add这份文件就宣布两项关闭。不要混入CRLF、他人文件或Codex审查测试；不要操作Codex审查worktree/历史证据。

保持2C维度/fingerprint和新Embedding生产fail-closed，2D仍待授权；不连接真实模型/DB/ES/Qdrant，不扩大Stage1历史审计、不做命名格式重构。

验证：用现成MSYS2 UCRT64进程级环境，干净新提交先跑两个真实失败条件和新增回归，再受影响包普通/race、准确-race-count10-v、串行全量、build、vet。对新涉controller包也做必要race。记录完整SHA/CWD/env/CMD/实际命中/EXIT；必须Git对象确认必要生产/测试已入提交，不借未跟踪文件计PASS。无DATA RACE不等于行为断言通过。

按已有提交推送授权处理，选择性提交，不强推不合并main，不更新独立验收通过状态。交付新完整SHA、两项修复的实际调用链/证据与测试归属，等待独立复核。
