# A-4 本机合成数据 Elasticsearch 观察

- 日期：2026-10-31（Asia/Shanghai）。用户明确授权创建一次性索引、写入一条合成文档，并执行只读 A-4 查询。未授权删除，资源保留。
- 项目基线 HEAD：38142d05d25e762e7058865213b9e667dee980db；包含未提交工作区，不能视为干净提交；只执行直接 ES 查询（L0），不是项目检索器的真实 ES 运行。
- 环境：Docker 容器 stage2d-a4-es，elasticsearch:8.11.3，127.0.0.1:9200，本机独立数据卷 stage2d-a4-es-data；ES 安全认证关闭，端口绑定本机。
- 目标索引：a4-probe-38142d0-20261031；创建前 GET 404 且 error.type=index_not_found_exception。创建请求 200 acknowledged，写入合成文档请求 201 created。未使用真实数据或模型服务。
- 本轮只读：GET / 200，版本 8.11.3；GET 索引创建前 404、创建后 200；_count 200，候选 1；_search 2 次。创建和写入各 1 次，均非只读，不自动重试。GET 3、_count 1、_search 2；不含创建及写入请求。
- 创建后 mapping：content_vector=dense_vector，dims=1024；qa_content_vector 不存在。单分片、0 副本；内容为自建合成文档，向量为 1024 维 one-hot。
- 对照查询：与项目核心过滤和 script_score 匹配，content_vector + cosineSimilarity，_source=false；HTTP 200，1 命中，failed_shards=0，timed_out=false，max_score=2。
- A-4 查询：相同请求仅将脚本字段切换为 qa_content_vector，_source=false；HTTP 400，error.type=search_phase_execution_exception，root_cause.type=script_exception。仅记录已观察的安全摘要，不保存正文、完整向量、原始错误 reason 或原始响应。
- 结论：在此 ES 8.11.3 单节点、单分片和单条合成数据样本下，缺 qa_content_vector 的直接查询返回上述错误；不能仅凭错误类型断言详细根因或推广到其他版本、数据、分片与部署。与项目请求的查询/过滤形状对应，但 _source=false 与项目实际响应形态不同。
- 限制：受控应用的 fingerprint/schema 门禁会拒绝缺字段 mapping；没有使用真实 embedding、真实项目检索器、生产资源或真实业务数据。项目检索器对该真实 ES 响应的处理未验证；L1 为此前源码/离线证据，L2/L3 未执行。A-4 的完整验收条件及 Stage 2D 尚不能由本样本单独判定通过。
- 索引和文档保留，未删除或清理；未提交、推送、合并或更新验收状态。ES 自身初始化系统元数据不等同由测试脚本创建业务资源。