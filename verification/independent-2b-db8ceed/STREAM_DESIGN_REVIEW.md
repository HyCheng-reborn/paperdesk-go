# Stage 2B 流生命周期方案复核建议

固定源码：db8ceede9bb0e7475a3bb5b5a82e853063da5d49。此为设计建议，不代表代码通过，也没有改变既有验收状态。未实施业务代码或运行模型/真实设施。

建议：选择方案B的受控版本，并一并修复无预算抢消费；不选A，不等待C，不直接扩大到D。

A仍以到达Send/退出defer为前提，没有解决阻塞源Recv。F只证明显式ctx取消能中断真实HTTP源，未证明能从raw reader.Close自动得到该取消信号。B中“必然改变Stage1 SSE cleanup契约”的说法过强：当前internal/logic/chat/chat.go cleanupFail已是cancel→通知失败→关闭前台分支→等待；cleanupOK是成功通知→等待后台EOF/保存→关闭前台→cancel。应保持此顺序，不把Provider句柄改造扩大为SSE协议或保存规则重做。

建议在Provider与业务之间增加显式拥有的流会话/清理句柄。返回reader及其唯一取消/等待能力，统一关闭操作负责取消、关闭其拥有的资源、等待转发和预算终结。实际接口命名可由实现者选择，须明确reader及Copy分支归谁关闭，避免重复close。Eino原Stream签名可作为兼容层保留；不能再声称raw *schema.StreamReader.Close单独保证中断HTTP。需将方案与验收条款明确调整到受控句柄的Close/cleanup上，不能悄悄降级成“测试多做一次外部cancel”。

受控Close必须有真实生产调用方；只定义新handle而Chat仍调用旧Stream不算完成。预算Provider迁移路径不得静默回退到无句柄实现。旧无Provider路径沿用既有Stage1取消协调，不要求重写所有历史模型。

不要依赖GC/finalizer；若保留finalizer，仅可兜底，不参与正确性验收。无预算也应采用同一单消费者所有权路径，或返回未被后台消费的原始流并提供明确取消句柄。提议的pipe转发可以避免两个消费者直接抢同一sr，但其本身不解决Close，且示例遇非EOF错误直接return会吞掉错误；实施时必须保留错误传播。

验收：真实本机SSE首条后保持连接；仅调用受控Close（不额外取消、不GC、不预先关源、保留reader引用）即令HTTP收到取消、任务退出、slot释放；百炼/OpenAI均覆盖。无预算ABC完整到达。保留Stage1失败不保存部分回答、成功EOF后保存一次及原SSE帧；Close/EOF/取消并发幂等，构造失败释放取消资源。受影响普通/race和关键十轮及build/vet/串行全量通过，所有必要测试进入新提交。

摘要中的实验A–F方向与已核对源码/原独立证据一致，但本次未获保留实验文件/日志路径，故不把摘要当新增独立执行证据；不要求为归档重跑整轮，只要求后续最小生产回归保留可复现源码与日志。

实施属于Provider流契约的明确调整，建议用户选择此方案后交Qoder专家模式执行；本记录本身不授权提交/推送、不宣布2B验收通过。
