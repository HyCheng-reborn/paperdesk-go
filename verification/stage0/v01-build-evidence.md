# V01 构建证据

## 环境

| 项目 | 值 |
|---|---|
| Go 版本 | go1.27.1 windows/amd64 |
| GOMODCACHE | C:\Users\zhy\go\pkg\mod |
| GOPATH | C:\Users\zhy\go |

## 命令与结果

### go build ./...
- 退出码：0
- 输出：无错误

### go vet ./...
- 退出码：0
- 输出：无警告

## 关键依赖版本

| 模块 | 版本 |
|---|---|
| github.com/cloudwego/eino | v0.6.0 |
| github.com/cloudwego/eino-ext/components/model/openai | v0.1.6-0.20251202024135-b0f57b984d19 |
| github.com/cloudwego/eino-ext/components/embedding/openai | v0.0.0-20251202024135-b0f57b984d19 |
| github.com/cloudwego/eino-ext/components/indexer/qdrant | v0.0.0-20251107064029-2e128d3d2258 |
| github.com/wangle201210/chat-history | v0.0.1 |
| github.com/gogf/gf/v2 | v2.9.0 |
| gorm.io/gorm | v1.30.0 |

## eino-ext 依赖图

| eino-ext 模块 | 要求 eino 核心版本 |
|---|---|
| embedding/openai | >= v0.6.0 |
| model/openai | >= v0.6.0 |
| indexer/qdrant | >= v0.4.1 |
| 其余 ext | >= v0.3.27 或 >= v0.3.31 |

## 结论

依赖解析和编译通过（`go build ./...` 退出码 0）。所有 eino-ext 要求的 eino 核心版本均 <= v0.6.0，MVS 选择 v0.6.0 满足全部依赖。运行时兼容性仍待后续 Stage 验证。
