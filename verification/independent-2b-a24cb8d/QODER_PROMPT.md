建议交给 Qoder 专家模式。本任务涉及真实发送预算、流生命周期、初始化原子性和并发注册，不能仅用Flash补文档或增加fake测试。

请基于完整目标 a24cb8d609e6c4580b0726cb7de0ccd21d2efdd1，集中修复 Stage 2B 独立复核阻塞。先读：
C:/Zhy/code/go-rag/verification/independent-2b-a24cb8d/REPORT.md
以及 SOURCE_EVIDENCE.md、reproduction.log、reproduction-hooks-race.log 和 repros/ 中完整最小复现。

六个根问题：
1. Budget仍只保存、不使用。实际Registry+ProcessBudget(MaxRequests=1)发送2次HTTP，账本全0。把进程共享owner、请求/任务累计owner、每次真实发送lease、deadline/输出上限及真实body/stream终结接入生产链；重试与后台移交不刷新累计，不重复结算。不用测试手写Finish来证明生产自动终结。
2. 新Embedding profile经真实ProviderConfig→buildRegistry→common.NewEmbedding即可调用。2C guard之前生产必须fail closed，同时允许明确未配置的新Embedding继续既有路径，以便其他角色独立迁移。无需提前实现整个2C；离线适配器构造测试仍允许。
3. Embedding经过锁定SDK后原始index/presence已经丢失。实现共享原始响应解析边界验证数量、index缺失/重复/越界/顺序、维度/有限值和usage presence。乱序可以明确拒绝或可靠重排，绝不能错配文本。usage缺失允许保守unknown，但不得退款为0。
4. 旧Chat/Rewrite singleton能覆盖新Registry，Chat工厂吞掉Registry错误并回退，真实Rag.Retrieve的Rerank仍走旧GetConf/NewRerank。统一默认/显式配置选择、传播Provider错误、将真实RerankRole注入检索点。保留旧Rerank校验内核；不要把Registry反向塞进NewRerank导致循环。排序URL单独处理，不能从Chat compatible-mode URL直接追加/reranks。读取provider错误必须传播。
5. SetRegistry早于core.New成功，失败会残留；Reset/Provider=nil初始化不处理旧Registry。使Registry/transport归属、发布、失败回滚和成功Reset清理一致；不重引Stage1持锁等待/超时强关，不刷新进程配额。
6. builder maps无同步，公开注册与构建已跑出真实DATA RACE。定义注册期/冻结或不可替换契约、同步读写、拒绝nil/不合约重复/未知Provider，不把未知Provider静默当OpenAI。

正常init只做内存注册、本批Rerank共享构造无递归，这两点已有独立成功证据，请保留，不为了避开审查推翻框架。

不要操作、清理或复制回 Codex 审查 worktree；其中有5个本轮未跟踪测试，旧测试在独立证据preserved/。只读参考复现；正式回归测试必须核实后进入你自己的交付提交，不依赖未跟踪文件。

测试必须经过生产共享配置/构造/解析/预算/关闭路径，以httptest观察真实HTTP次数、阻塞body与真实stream终结，覆盖本报告复现。现有StreamInterrupted helper会写[DONE]，必须修正fixture并验证真正中断的结果；不要仅断言不panic。不得用可变包级函数替换复制实现。

保持离线：不调用模型API，不连接真实DB/ES/Qdrant，不更新索引或数据，不升级依赖。采用现有授权MSYS2 UCRT64工具链，仅进程PATH/CC/CGO设置；无需系统排障。

交付前：受影响包普通与race；providers/... race；common/rerank/logic/rag/logic/chat等实际影响包race；准确关键预算、流关闭及注册/构造并发正则十轮race（确认命中）；一次串行全量/build/vet。保存新日志完整SHA/CMD/CWD/环境/EXIT，区分顶层/子项数量。原干净测试通过不能替代新增真实链路复现。

集中交付完整SHA、修复范围、逐项证据、实际角色接线与剩余边界。不要宣称独立复核已通过或整个Stage2完成；提交/推送只按已有明确授权，不合并main。
