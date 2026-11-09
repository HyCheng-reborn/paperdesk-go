# Stage 2D A-4 PR #2 增量复核修复

固定父提交 `345786564384df40fd0b0378206f26c85b6a4a5b`；PR base `4654cb012ea4e16444b41182b30fcda8d68b6457`。本批只在本机 fake/httptest 验证；历史 L0/L2 报告未改，不代表本提交在线验证。A-4 和 Stage 2D 验收状态不变，待增量独立复核。

- 传输层捕获每次 Retrieve 上下文关联的 HTTP 状态与响应元信息；HTTP 和 body 状态冲突、5xx、超时、分片失败、不可判读均不产生完整观察。对照失败即停。读取响应最多 1 MiB + 1 字节；超限或读取错误关闭原响应并失败，不向 SDK 交付截断响应；成功时以原样字节供 SDK 解析并由 SDK 关闭。
- URL 配置和客户端构造错误不回显敏感输入；错误类型只输出白名单或 unknown。维度经完整整数解析，范围 1–4096，默认 1024，摘要采用当次目标维度。
- PR 范围说明改正树外清单引用；新增回归文件按独立复核描述重建，非复核方原件。未修改 SDK 或应用检索架构。

## 离线验证

Windows、Go 1.27.1、UCRT64 gcc；`GOPROXY=off`、`GOSUMDB=off`。真实目标环境变量未设置，`TestA4Component_RealESTarget` 默认 SKIP。测试采用生产 `newRetriever` 与 es8 SDK、仅访问 httptest。

- 基线 overlay：以固定父提交的两份组件测试文件替换工作区同名文件，运行 `go test -overlay <baseline-repro/overlay.json> -p 1 ./core/retriever -run '^TestA4Review_' -count=1 -timeout 120s -v`：exit 1，状态冲突、部分响应、URL/类型脱敏、非法维度和 512 维摘要的复现断言失败；正常空 QA 对照通过。补充断言为按复核描述重建。
- `go test -p 1 ./core/retriever ./core -run '^TestA4' -count=1 -timeout 120s`：exit 0。含真实入口默认 SKIP、四次限额、503 禁重试、3xx 禁跟随、取消、响应关闭与新增复现。
- `go test -race -p 1 ./core/retriever ./core -run '^TestA4' -count=1 -timeout 180s`：exit 0。
- `go test -race -p 1 ./core/retriever -run '^TestA4Review_ConcurrentResponseAssociation$' -count=5 -timeout 180s`：exit 0。
- `go test -race -p 1 ./core/retriever ./core -count=1 -timeout 180s`：exit 0。
- `go test -p 1 ./core/indexer ./core/retriever -run '^TestDimensions_' -count=1`：exit 0。
- `go test -p 1 ./... -count=1`、`go build ./...`、`go vet ./...`：均 exit 0。

受限环境中首次测试曾出现编译器标准库读取失败，以及本机假服务连接失败；改用允许访问本机编译缓存的执行环境后复跑，上述结果均为后者，不把环境失败计作缺陷复现。

## 原始基线核对

本地只读比较原始 `38142d05d25e762e7058865213b9e667dee980db` 与公开 base 的文件树：仅 `verification/stage2c3-fix2/focused-transient-exec-denied.log`、`verification/stage2c3-fix2/focused-transient-tempfile-locked.log` 和 `verification/stage2d-offline-a12af92-20261028/environment.log` 三文件不同；将两侧用户目录名屏蔽后，各文件逐行一致。没有上传原始对象或更改 base。
