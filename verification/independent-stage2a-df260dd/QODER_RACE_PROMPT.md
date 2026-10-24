建议模式：Qoder qwen3.8flash（本任务仅执行固定补证清单并整理日志，不进行根因排障或代码设计）。

Stage 2A 源码修复已由独立复核确认闭合，目标为 df260ddfb12694e7064d74fb63cc1c228f8ebbfe。目前仅 E1 race 证据缺口未完成。请先读：
C:/Zhy/code/go-rag/verification/independent-stage2a-df260dd/REPORT.md

不要再修改业务代码、测试语义、方案或验收状态。不要操作 Codex 审查 worktree，也不要重跑 Stage 1 历史压测。

仅在用户已经提供/授权的、现成可运行 Go race 的环境中，对干净固定目标执行：
1. go test -race ./core/providers/... -count=1 -timeout=180s
2. 使用报告所列20项完整准确正则，执行 go test -race ./core/providers/... -run 正则 -count=10 -timeout=180s -v

开始前核实完整 SHA、工作区和 Go/CGO/C 编译器环境；不得把普通测试当 race，不得把 no tests to run 当通过。日志记录完整 CMD/CWD/环境/退出码/实际 RUN 名称，并与旧环境失败日志分开。

若没有现成获授权的可运行环境，或同环境仍报0xc0000139，保持“运行异常、race未完成、根因待确认”，停止并报告缺口。不要安装/升级工具链、搭建WSL、修改系统或反复排障；不要仅凭版本断言根因，也不要再制造业务修复提交。

不连接真实设施、不调用模型API。完成仅交付证据和剩余状态，待独立复核后再决定验收；不宣布Stage 2完成。无需为本任务提交或推送。
