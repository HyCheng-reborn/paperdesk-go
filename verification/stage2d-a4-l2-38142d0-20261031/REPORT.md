# Stage 2D A-4 L2 本机真实 ES 组件观察

- 日期：2026-10-31（Asia/Shanghai）。用户在同一对话明确授权执行一次本机 L2 只读验证，范围含一次 UUID 获取和最多四条组件请求；不创建、写入或删除资源，不调用模型。
- 环境：本机 Docker 容器 stage2d-a4-es，镜像 elasticsearch:8.11.3，127.0.0.1:9200；容器状态和端口由 Docker 只读核实。目标索引 a4-probe-38142d0-20261031、过滤值 a4-synthetic-kb，均为此前用户授权创建的合成数据；未使用真实业务数据。
- 项目对象：HEAD 38142d05d25e762e7058865213b9e667dee980db 加未提交工作区，非干净提交、未冻结整个工作区。运行前核实：design/stage2d-a4-l2-component.md SHA256 99D512CF21F95A17E02F34EBE9185B1AC684547363064EA1DCED5BB504B3BAD3；server/core/retriever/a4_component_real_branch_test.go FDA247370C6FFA47028771978B974AF0E8D8CAD8BFB6841E67AFBA5C9CF0E834；server/core/retriever/a4_component_entry_test.go 23F903E1CB04D0663EF92EF752C47FA0A7EABF1A24194804AD65FDF5F72C239F。其他文件未逐项冻结。
- 先 GET 单一具体索引的 UUID 与 aliases：HTTP 200；完整 UUID 仅在进程内使用，不输出、不归档。
- 实际命令（环境变量为本机目标、授权开关、1024 维、合成知识库和本进程取得的 UUID）：go test -p 1 ./core/retriever -run '^TestA4Component_RealESTarget$' -count=1 -timeout 120s -v；GOPROXY=off，GOSUMDB=off，NO_PROXY 为本机；exit 0。命令执行前后仅在子进程设置并清理 A4_COMPONENT_*。未设置任何真实模型凭据。
- 组件报告：requests=4，refused=0；index_ok=true，aliases_empty=true，uuid_match=true；content_exist=true、content_dims=1024、qa_exist=false；fake embedding 调用 2 次，query_vec=e0/1024；observed=true、incomplete=false。
- 对照 content_vector：kind=ok，status=200，hits=1。QA 查询：kind=script_rejected，status=400，hits=0，error.type=search_phase_execution_exception，root_cause.type=script_exception，cause_not_attributed。组件从真实 ES 响应得到并分类了错误；不仅凭状态/类型断定详细根因。
- 本轮 ES 请求总计 5：前置 UUID GET 1 + 组件预检 GET 2 + _search 2；无重试，未写入 ES。组件生产形状未过滤 _source，合成样本在本机内存接收；报告不存原始响应、完整向量、正文或 UUID。
- 结论边界：这是一次 L2 组件级观察，使用真实 ES 和本机 fake embedding，绕过应用 fingerprint/schema 门禁，不经过完整双路融合/RRF/rerank，不证明应用全链 L3 行为。L0 报告见 verification/stage2d-a4-local-es-38142d0-20261031/REPORT.md；L1 为此前静态及离线证据。A-4 完整验收、Stage 2D 和其他 ES 版本/数据环境是否通过未由本次自动决定。
- 测试后合成索引与容器保留，未删除；未提交、推送、合并、修改门禁或更新验收状态。
- 身份限定：本轮前置 GET 与组件预检的完整 UUID 在本进程内精确一致；L0 报告未保存完整 UUID，因此本轮不能独立证明当前索引与 L0 执行时是同一物理 incarnation。
