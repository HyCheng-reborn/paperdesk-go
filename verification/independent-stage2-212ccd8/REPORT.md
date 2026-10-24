# Stage 2 预算规则增量独立设计复核

日期：2026-10-19

结论：**可开始限定范围的首批 2A；本次 R1/R2 设计阻塞已闭合，未发现新增的首批编码阻塞。** 这是设计可实施性结论，不代表预算实现、生产接线、在线能力或 Stage 2 已验收。

## 目标与方法

- 完整目标：212ccd8cc4c5743e6e34dfe5cb73e02b7a794b84。
- 增量基线：0d0bab9e966065dbd6e41528bd5291c9a5c6f30f。
- 原始方案：3bde7cd881672d54b99aef0cd8d02a27cc635d6d；原始代码基线：69984c4038d3bc04f822bdc6e31e40af73e3dc44。
- 复用 linked worktree：C:/Zhy/code/go-rag-review-1d3。最终 HEAD 为目标，git status --short 无输出。历史复现与报告保持原处。
- 检查本次三份文档差异、方案预算/接口/验收条款，沿用前轮锁定依赖及生产调用链证据；没有重新开展 Stage 1 审计。
- 本次未运行 Go 测试：变更仅文档，本次修订无需最小编译验证。未调用模型 API、未启动真实设施、未修改生产代码或方案、未提交推送，也未更改验收状态。

## 原阻塞处理

### R1：发送前尝试预留及累计 owner —— 已闭合

证据：design/stage2-plan.md §4.1（222–231 行）、§4.3–4.5（248–285 行）、§4.9（326–337 行），§7.1 与 §9 验收表。

两个作用域在 sender 之前整体预留 slot、token 和一次尝试；不足时不发送并完整回退。reserved/committed 区分允许未承诺预留回滚，同时禁止发送失败返还尝试。ProcessBudget 由进程 owner 保持，不能通过换 Registry 刷新；请求/任务累计 owner 与单次 AttemptLease 分离。ChatStream 子步骤共享 owner，Index 同步和后台显式移交同一累计 owner，保留 Stage 1 后台 root/timeout 和 HTTP 取消隔离。

验收已包含 MaxRequests=1 并发实际发送一次、双作用域拒绝零发送、重试/子步骤/后台不刷新额度。设计不再允许原来的并发超发和网络错误退款绕过。

### R2：body/stream 终结与并发槽 —— 已闭合

证据：同文件 §3.7（210 行）、§4.1、§4.5–4.7（277–317 行），§7.1 与 §9。

非流式以 body 消费并关闭或错误/取消后关闭为边界，不再把 Do 返回响应头视为完成。流式 EOF、终止性读错、取消后关闭、提前 Close 均可终结，不要求 EOF 和 Close 同时成立。共享幂等 finalize 释放 slot；token 保守保留与 slot 释放解耦。重复 Finish/Abort/Close、Close/EOF 并发不重复结算。

这与前轮检查的 Go net/http 生命周期相容：Do 返回后仍由调用者消费和关闭 Body。方案也明确透明包装不得抢读或新增裸 goroutine，不改变 Stage 1 SSE 关闭所有权。

## 准许开始的 2A 范围与验收

1. 不可变 Profile、角色/capabilities、锁定 Eino API 的适配器和明确构造入口。至少一条可复用生产共享链；测试不得复制预算实现或仅测试空接口。
2. ProcessBudget、RequestBudget/TaskBudget、AttemptLease 共享执行核心；原子双作用域预留、Commit、一次性终结、deadline/取消、有限重试及后台 owner 移交。
3. 按 §7.1/§9 运行离线 fake/httptest 与 race：实际 sender 次数、双作用域完整回退、累计不刷新、失败不退尝试、阻塞 body、终止路径释放、unknown usage 保留 token，以及并发终结只结算一次。
4. 验证现有模型到新适配器的构造/调用路径。默认全业务接线由 2B 承接；2A 完成不能宣称全应用请求已受预算保护。

## 后续批次门禁与探针边界

这些事项不阻塞 2A，但不能因本次预算规则通过而略过：

- 2B：原始 Embedding 响应的数量/index/顺序、维度/有限值和 usage presence 校验；协议专属 encoder/decoder；实际默认工厂/业务接线；沿用 Rerank 已验收校验链。
- 新 Embedding profile 生产启用必须等待 2C 的模型身份/维度 guard 就绪，或将必要 guard 前移。不可在 guard 缺席时仅按批次名提前启用。
- 2C：ES/Qdrant 实际建索引、写入和检索入口全覆盖；fingerprint 存取与旧索引策略明确。维度相同不代表模型相同；不匹配须在访问前阻断，不自动重建或删除用户索引。
- 实际 usage、流中断及服务/模型可用性留给获授权的最小 2D 探针；不要求离线编码前消除这些未知项。A-4、真实向量库/ES 启动兼容与付费模型探针分开安排。

## 非阻塞实施注意事项

- §4.4 的 atomic.Add 示例不是整体事务算法。实现需要统一锁顺序或等价协调机制，确保多作用域和多资源的准入/回退契约；检查负数、溢出及 Commit/Abort 竞争。
- Finish/Abort/Close 必须共享同一 lease 终结状态，不能每个方法各有一个互不关联的 Once。slot 和 token 各自至多处理一次。
- 若按 §4.5 采用异步 token 结算，保留未结算预留，纳入 owner 的完成/关闭边界，不能靠未登记 goroutine 延后记账；优先采用无需额外后台工作的实现。
- §3.3/§7.1 对兼容层接入时点仍有文字交叠：按本报告限定首批执行，实际默认工厂切换在 2B；回滚应恢复实际变更的兼容层，不能机械地只删目录。

## 证据与命令

前轮 API/调用链证据：verification/independent-stage2-3bde7cd/、verification/independent-stage2-0d0bab9/。本轮不把历史测试或实现者摘要当作预算实现已通过的证明。

本轮 Git 核验（PowerShell；工作区 C:/Zhy/code/go-rag；显式 -C 指定审查目录）：

- git -C C:/Zhy/code/go-rag-review-1d3 rev-parse HEAD：exit 0，输出目标完整 SHA。
- git -C C:/Zhy/code/go-rag-review-1d3 status --short：exit 0，无输出。
- git -C C:/Zhy/code/go-rag-review-1d3 diff 0d0bab9e966065dbd6e41528bd5291c9a5c6f30f 212ccd8cc4c5743e6e34dfe5cb73e02b7a794b84 --stat：exit 0，三份文档，134 insertions、39 deletions。

普通沙箱首次针对 sibling worktree 的 Git 只读核验返回 Permission denied（exit 1），随后以已授权的扩大沙箱权限重跑成功；不属于项目运行/测试失败。
