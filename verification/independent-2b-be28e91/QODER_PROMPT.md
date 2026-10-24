使用模式：Qoder 专家模式。

请修复 Stage 2B 独立复核阻塞。基线固定 be28e918f4962e88906919e6f45ce39707cb9a50；先读 verification/independent-2b-be28e91/REPORT.md 和其中证据。不要修改、切换、清理 Codex 审查 worktree；复现文件及历史证据由复核方保留。不要把 overlay 补测算作提交自带通过。

本轮目标是最小修复真实正确性和交付完整性，不扩大到2C/2D，不升级依赖，不连接真实设施或调用模型，不因格式文档另开重构。

1. 补齐遗漏生产/测试依赖：BuildConfig.Budget、FakeRegistry接口、ProviderConfig预算字段和缺失测试helper。审查原工作区未提交内容后选择性纳入，不 git add 全部，不把 CRLF/他人文件混入。必须在干净提交树能够 build/vet/test；不能靠 fake TestMain 隐藏真实 builder 缺失。
2. 修正预算实际接线：入口请求/任务累计 owner 复用及后台显式移交；进程 owner 按既定范围共享。真实发送前计 attempts，处理/禁止 HTTP redirects 与隐藏重发绕过；本地输入错误不能 Commit。输出上限与预留一致，不能预留4096却发8192。Embedding 收到已知合法 usage 必须结算，不能永远 Finish(nil)。保留2A双作用域、溢出保护和幂等规则。
3. 删除流式 Copy(2)+后台消费 usage 的失控所有权；采用单一共享生产 Recv/Close 包装，确保提前Close/取消/EOF/错误终结源和lease，无额外无法等待退出的消费者。读错不使用部分usage不当退款；保持Stage1 SSE和关闭契约。
4. Reset 在 Shutdown 失败时保留包括Registry在内的精确引用；成功后按实例身份/初始化代次及既有锁规则清理。新实例构造显式使用本次Registry，成功才发布、失败不污染旧实例。不能为避免早发布让构造继续查旧全局。
5. 明确 cfg=nil 默认路径与显式配置语义，不无条件丢弃显式cfg。真实Rerank已配置Registry时，GetReranker错误必须传播，禁止静默改走旧配置/预算；未配置兼容另行识别。
6. 恢复实际支持的 OpenAI 生产 builder，验证生产导入装配；保留builder map同步、冻结与nil/重复检查。fake builder只能做局部单测，不能替代真实构造证明。
7. Embedding包装支持合法nil Client.Transport并保留客户端策略/关闭责任；Rerank先验证nil文档等输入再估算/扣预算，保留可识别错误与既有HTTP边界。

保留已正确修复项：Embedding生产guard fail closed、SDK前原始index验证、默认旧缓存不盖Registry、Chat factory错误传播、初始化失败不发布、Rerank无递归和builder同步。

测试必须走生产共享路径。将报告中独立复现条件转为适当回归，不机械复制私有fixture；尤其真实HTTP307发送计数、输出max_tokens、已知Embedding usage、提前Close、真实Shutdown超时保留Registry、显式cfg、真实OpenAI构造、合法默认Client及nil文档。并发用信号、限时等待、cleanup，等待所有启动任务结束。不能用手工lease操作或空快照验证HTTP/Stream。

在干净新提交中，用现有已授权MSYS2 UCRT64进程级环境运行受影响包普通/race，关键真实发送/流终结/注册构造并发准确正则-race -count=10 -v，及 go test -p 1 ./... -count=1、go build ./...、go vet ./...。保存完整SHA/CWD/命令/环境/命中名/退出码到新证据目录。遇编译失败或RUN=0如实记失败，不以旧工作区日志替代。

交付完整新SHA、相对基线文件、每项修复与实际生产链、真实测试结果和2C/2D边界。按现有项目授权处理提交/推送，不强推不合并main；不要宣布独立复核通过或更新验收通过状态。修复后等待独立复核。
