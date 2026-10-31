# Stage 2D A-4-only PR 范围说明

- 目的：说明本 PR 只交付 A-4 子集；给出 A-4 验收条款的**准确引用**；说明树内文档与树外文档的版本差异，避免把未随本 PR 交付的功能读成本 PR 已实现。
- 生成日期：2026-10-31（Asia/Shanghai）。来源代码基线：`38142d05d25e762e7058865213b9e667dee980db`；公开审阅用基线是从该提交的文件树制作的脱敏快照 `4654cb012ea4e16444b41182b30fcda8d68b6457`，仅对三份历史日志中的本机用户名路径作脱敏，保留现有公开分支为父提交；它不是原始基线提交，也不包含原始基线新增九个提交的逐条历史。
- 本文档是新增文件，不修改任何既有文档；A-4 与 Stage 2D 的验收状态不因本 PR 改变。

## 1. 本 PR 交付什么

| 内容 | 说明 |
| --- | --- |
| 4 个 A-4 测试文件 | `server/core/retriever/a4_component_entry_test.go`、`a4_component_real_branch_test.go`、`a4_query_shape_test.go`、`server/core/a4_component_rag_test.go` |
| 1 个共享测试 helper 改动 | `server/internal/testutil/dimensions.go`：fake embedding 由全零改为非零 one-hot，新增 `OneHotVector`/`EmbeddingHotIndex`；A-4 测试依赖这两个符号（缺失则无法编译）。这是**共享 helper 行为变化**，已回归 `core/indexer`、`core/retriever` 的既有维度测试 |
| 2 份设计文档 | `design/stage2d-a4-es-plan.md`（L0 方案与证据口径）、`design/stage2d-a4-l2-component.md`（L2 组件入口与子用例） |
| 3 份观察/台账报告 | `verification/stage2d-a4-local-es-…`（L0）、`verification/stage2d-a4-l2-…`（L2）、`verification/stage2d-a4-ledger-…`（台账） |
| 1 份 PR 清单 | `verification/stage2d-a4-pr-…-v2/`（清单、哈希、PR 正文草稿） |
| 本说明 | 本文 |

## 2. 验收条款引用（本 PR 树内可直接核对）

以下条款所在文件 `design/stage2-plan.md` 已在基线中跟踪且本 PR 未修改：工作区 SHA256 `9766F7542F7AC4C883AA968CFF0153C55FF8C67C24AF6308F9A0ADD3BD911B1E`，70292 B，git blob `91bd81bbda0e7f68312237ae16dd68c975499c9a`（69407 B）。

### 2.1 §8.8「A-4 缺 QA 向量检索行为」（第 781–785 行）

- **已确认**：QA 关闭时 `qa_content_vector` 字段在 ES mapping 中可能缺失；检索器 `retrieveDoOnce` 中 `x.retrieve(ctx, req, true)` 使用该字段做 kNN。
- **待探针确认**：ES 在 `qa_content_vector` 字段缺失时的 kNN 行为（报错？空结果？）。
- **设计决定**：2D 探针包含一项验证：QA 关闭状态下检索器的行为。

### 2.2 §9.1 2D 行（第 804 行）

验收列含「A-4 QA 向量行为记录」；回滚列含「不影响离线代码（探针为只读验证）」。即 A-4 的通过口径是**记录行为**，不是"必须报错"。

## 3. A-4 判据引用（树外版本，需配合版本标识阅读）

Stage 2D 计划文件 `design/stage2d-plan.md` 在本 PR 树内是**基线提交版本**，其 P-8 行仍是被取代的旧口径（见 §4）。因此 A-4 的判据一律引用**工作区版本 v1.4**，该版本**不在本 PR 树内**：

- 版本标识：工作区 SHA256 `68A5F6CC2D8FE0D3D94AB6D608ECEEC63B48B94EE983F9B5E10891FCA9C4F439`（93933 B，CRLF），git blob `0d94bf2e6331df8167cc1f3b1323d8da162bd76a`（93408 B）。
- 下文行号均指该版本（526 行）。

| 位置 | 引文（节选） | 作用 |
| --- | --- | --- |
| §2 P-8（L89） | 「A-4：QA 关闭时 `qa_content_vector` 字段缺失场景下真实 ES kNN 行为；**不属于本次 S-1，但为原计划完整 2D 验收条件**」；通过标准：「行为被观测并记录（报错/空结果/其他），与检索器错误路径一致」；失败处理：「行为导致静默错误结果 → 记录为缺陷候选，不自行修复」；证据：「ES 响应/错误摘要。**属真实 ES 验证项，不与付费模型调用混排为同一授权；尚未执行或授权**」 | A-4 的判据与证据口径 |
| §5.2（L270–271） | 「P-8/A-4（完整 2D 验收条件；不随 S-1 自动授权）」；「若本阶段单独执行只读 A-4，须在授权文本中明确"不写入"及其独立前提，此时本项不适用，**A-4 原验收要求不作削减**」 | 只读单独授权的分支，以及"不削减"约束 |
| §9 表（L359） | 「**P-8/A-4 …仍是完整 2D 验收条件**（`design/stage2-plan.md` §8.8、§9.1），**不因 S-1 两批样本而通过，也不改为"可选"**」 | 排除"可选"口径 |
| §9.3（L509） | 「**真实 ES / A-4（P-8）**：未连接真实 ES…本机 fake ES 是 `httptest` 本机 HTTP，不经 `sendGate`，不证明真实 ES 行为」 | 离线证据的边界；该句为 v1.4 时点 |

> 关于 L89「尚未执行或授权」：该短语是 v1.4 时点的表述。此后 L0/L2 两次观察是在**单独授权**下执行的（见台账与两份报告），授权口径符合 §5.2 的"单独只读"分支；但**这不构成 A-4 通过**，验收条件与缺口（G1–G7）照旧。

## 4. 树内 `design/stage2d-plan.md` 的已知差异（为何本 PR 不带整份）

| 维度 | 本 PR 树内版本（基线提交） | 工作区版本 v1.4 |
| --- | --- | --- |
| 标识 | git blob `e56c688a5adcee28bfe2128a1ef2b386b37454bb`，63930 B，375 行 | SHA256 `68A5F6CC…`（93933 B），blob `0d94bf2e…`（93408 B），526 行 |
| P-8 行 | 第 77 行：「（**可选**，范围由 U-2 决定）A-4：…」 | 第 89 行：去掉"可选"，明确为完整 2D 验收条件 |
| §9.3 | **不存在**（全文无 `9.3`） | 存在（L505–509，未验证边界汇总） |
| fix6–fix10 | 未登记实现状态 | §9 登记为"已实现、离线验证通过"（其代码**不在本 PR**） |

结论：

1. 树内版本的 P-8 行是**已被 v1.2 取代**的"可选"口径，若让读者只读树内文件，会得到与 A-4 现状相反的判断。故本 PR **不以树内该行作为判据**，A-4 判据以 §3 的 v1.4 引文为准。
2. 工作区版本同时登记了 fix6–fix10 的离线实现，其代码不在本 PR；整份带入会把未交付的功能写成"已包含"。故本 PR **不带整份混合文档**，改由本文提供一致的 A-4 说明。
3. 本 PR 不修改工作区的 `design/stage2d-plan.md`，也不修改任何历史报告。

## 5. 本 PR 不声称什么

- 不声称 fix6–fix10、受控流会话、预算接线随本 PR 交付或在本 PR 树内可用。
- 不声称 L3（`core.Rag.Retrieve` 全链）已验证：门禁会先拒绝缺字段索引，本 PR 只有静态与离线 fake 证据。
- 不声称其他 ES 版本、分片/副本配置、真实数据、真实 embedding/模型、Qdrant/MySQL、账单已验证。
- 不声称 A-4 或 Stage 2D 通过验收；验收状态与台账缺口（G1–G7）不变。
- L0/L2 是**含未提交工作区的历史观察**，不是本 PR 树的在线实测；本 PR 树未连接任何真实 ES。

## 6. 引用映射（A-4 文档 → 版本）

| 引用位置 | 被引用条款 | 指向版本 | 是否在本 PR 树内 |
| --- | --- | --- | --- |
| `design/stage2d-a4-es-plan.md` §1 依据表（§8.8、§9.1 行） | `stage2-plan.md` §8.8 / §9.1 | 基线提交版本 | 是 |
| 同上（§2 P-8 L89、§5.2 L270–271、§9 L359、§9.3 L509 行） | `stage2d-plan.md` | 工作区 v1.4 | **否**（见 §3） |
| 同上（L31、L301） | `stage2d-plan.md` §5.2 L271（单独只读分支） | 工作区 v1.4 | **否** |
| `verification/stage2d-a4-ledger-…/REPORT.md`（L14、L56、L58、L59） | `stage2d-plan.md` §2 P-8 / §5.2 / §9.3 | 工作区 v1.4 | **否** |

## 7. 证据分层

| 层 | 对象 | 能证明 | 不能证明 |
| --- | --- | --- | --- |
| L0 | 本机合成 ES 直接 DSL 查询（ES 8.11.3，单节点单分片，1 条合成文档） | 缺 `qa_content_vector` 时该 DSL 返回 400 `script_exception`；对照查询 200/1 命中 | 项目检索器路径；其他版本/数据/部署 |
| L2 | 组件入口 `TestA4Component_RealESTarget`（fake embedding，绕开门禁） | 组件从真实 ES 响应得到并分类了错误；`requests=4`、`refused=0` | L3 全链；融合/RRF/rerank |
| 离线 | 本 PR 树的 fake ES 测试（含 `TargetGate` 13 条拒绝子用例） | 5 个代码对象在离线用例下通过（含 `-race`） | 任何在线行为 |
| 未验证 | L3、其他 ES 部署、真实模型与账单、CI 环境 | — | 不得据此更新验收 |

## 8. 版本与哈希（本 PR 涉及的设计文档）

| 文件 | git blob sha1 | blob 字节 |
| --- | --- | --- |
| `design/stage2-plan.md`（基线内，未修改） | `91bd81bbda0e7f68312237ae16dd68c975499c9a` | 69407 |
| `design/stage2d-plan.md`（基线内，未修改） | `e56c688a5adcee28bfe2128a1ef2b386b37454bb` | 63930 |
| `design/stage2d-a4-es-plan.md`（新增） | `21f415c876fcdc1f2472697510f936b6136459a1` | 56422 |
| `design/stage2d-a4-l2-component.md`（新增） | `768ab2a4fee3015a1e07b11bdf6a548a50fc247b` | 24872 |
| `design/stage2d-a4-pr-scope.md`（本文，新增） | 见 PR 清单 `hashes.tsv` | — |
