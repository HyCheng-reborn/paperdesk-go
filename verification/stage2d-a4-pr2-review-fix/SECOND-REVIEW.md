# PR #2 第二轮增量修复

对象为父提交 `7bfa66cd8963e033eb57c7ca269e107217f4d6dd`；补充复现按独立复核描述重建，非复核方原文件。仅使用 httptest；真实 ES 入口默认跳过。

- 索引和 mapping 预检读取限制均增加一字节探测：超 1 MiB 报错，响应体仍由原有 defer 关闭，后续查询不执行。
- 成功 search 响应须含 `hits` 对象及 `hits.hits` 数组；空数组合法，缺失/null/非数组不可判为完整观察。SDK 仍接收完整原始响应。
- 重建反例在父提交上运行 `go test -p 1 ./core/retriever -run '^TestA4SecondReview' -count=1 -v`：exit 1，索引、mapping 截断及缺失 hits 均复现；修复后 A-4 用例通过。
- `go test -p 1 ./core/retriever ./core -run '^TestA4' -count=1 -timeout 120s` 与同命令加 `-race`、`-timeout 180s`：exit 0。真实目标保持默认 SKIP。
- `go test -p 1 ./... -count=1`、`go build ./...`、`go vet ./...`：exit 0；Go 1.27.1 Windows/amd64，race 使用 UCRT64 gcc，GOPROXY/GOSUMDB 关闭。

本批不改变应用检索器、SDK、历史 L0/L2 证据或验收状态。响应接收完成后、SDK 解析前取消是否优先于结果仍是待决策略，本批不作保证。
