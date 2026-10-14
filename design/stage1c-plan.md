# Stage 1C 细化实施计划

## 概述

Stage 1C 目标：修复 Chat 流式问答中的 context 丢失、EOF/错误处理缺陷和 SSE 写入语义问题。

**范围**：
- F03：`GetAnswerStream` 将请求 ctx 替换为 `context.Background()`，用户取消后模型/保存协程继续工作
- F04：`SteamResponse` 读流失败后仍发 `[DONE]` 并返回 nil；Controller 建流失败可返回空成功

**不修改**：依赖版本、数据库结构、ES 镜像。
**不调用**：真实模型或外部服务。

**前置条件**：Stage 1A 已完成（确保测试不触发真实连接）

**基线**：`eb27ba2ed6638f2d4d717bce1305dac92ff6b0ae`（Stage 1B-3 完成后的 HEAD）

## 调用链追踪

### 完整调用链

```
HTTP 请求
  │
  ▼
ControllerV1.ChatStream(ctx, req)          ← rag_v1_chat_stream.go:14
  │  ctx = GoFrame HTTP request context（客户端断开时取消）
  │
  ├─ c.Retriever(ctx, ...)                 ← 检索阶段，ctx 正确传递
  │
  ├─ chat.GetChat()                        ← 获取 Chat 实例
  │
  ├─ chatI.GetAnswerStream(ctx, ...)       ← chat.go:197
  │     │
  │     ├─ x.docsMessages(ctx, ...)        ← 构建消息（含历史）
  │     │
  │     ├─ ctx = context.Background()      ← ★ F03: context 被替换！
  │     │
  │     ├─ stream(ctx, x.cm, messages)     ← 调用模型 Stream
  │     │     └─ llm.Stream(ctx, in)       ← 使用 Background ctx
  │     │
  │     ├─ streamData.Copy(2)              ← 复制流为两份
  │     │
  │     ├─ goroutine: srs[1] 消费        ← 后台保存消息
  │     │     └─ ctx.Done() 监听          ← 监听 Background，永远不会取消
  │     │
  │     └─ return srs[0]                  ← 返回给 controller
  │
  └─ common.SteamResponse(ctx, srs[0], docs) ← stream.go:23
        │  ctx = 原始 HTTP ctx（正确）
        │
        ├─ streamReader.Recv()             ← 循环读取流 chunk
        │   ├─ io.EOF → break              ← 正常结束
        │   └─ err != nil → writeSSEError + break ← 错误
        │
        ├─ writeSSEData(...)               ← 发送每个 chunk
        │
        └─ writeSSEDone(...)               ← ★ F04: 总是发送 [DONE]，即使出错
```

### context 传递分析

| 位置 | ctx 来源 | 问题 |
|---|---|---|
| Controller.ChatStream | GoFrame HTTP ctx | ✅ 正确，客户端断开时取消 |
| c.Retriever(ctx, ...) | HTTP ctx | ✅ 正确 |
| chatI.GetAnswerStream(ctx, ...) | HTTP ctx → **`context.Background()`** | ❌ **F03: 第 202 行替换** |
| stream(ctx, x.cm, messages) | **Background ctx** | ❌ 模型调用无法取消 |
| goroutine 内的 ctx.Done() | **Background ctx** | ❌ 永远不会触发 |
| common.SteamResponse(ctx, ...) | HTTP ctx | ✅ 正确（但 writeSSEError 内部用 Background 记日志） |
| writeSSEError 内 g.Log().Error | **`context.Background()`** | ⚠️ 日志丢失请求关联 |

---

## 问题清单与源码证据

### F03-1：GetAnswerStream 替换调用方 context

**文件**：`server/internal/logic/chat/chat.go`
**函数**：`GetAnswerStream`
**行号**：202

```go
func (x *Chat) GetAnswerStream(ctx context.Context, convID string, docs []*schema.Document, question string) (answer *schema.StreamReader[*schema.Message], err error) {
    messages, err := x.docsMessages(ctx, convID, docs, question)
    if err != nil {
        return
    }
    ctx = context.Background()  // ← ★ F03-1: 替换了调用方 ctx
    streamData, err := stream(ctx, x.cm, messages)
```

**问题**：
- 第 202 行 `ctx = context.Background()` 丢弃了 HTTP 请求 context
- 模型 `Stream` 调用使用 Background ctx，客户端断开后模型继续工作
- 无法传递超时或取消信号给模型

**影响**：用户关闭浏览器后，模型调用继续消耗资源，后台保存 goroutine 继续运行。

### F03-2：后台保存 goroutine 监听无效 context

**文件**：`server/internal/logic/chat/chat.go`
**函数**：`GetAnswerStream`
**行号**：208-240

```go
go func() {
    fullMsgs := make([]*schema.Message, 0)
    defer func() {
        srs[1].Close()
        fullMsg, err := schema.ConcatMessages(fullMsgs)
        if err != nil {
            g.Log().Error(ctx, "error concatenating messages: %v", err)
            return
        }
        err = x.eh.SaveMessage(fullMsg, convID)
        if err != nil {
            g.Log().Error(ctx, "save assistant message err: %v", err)
            return
        }
    }()
outer:
    for {
        select {
        case <-ctx.Done():           // ← ★ F03-2: ctx 是 Background，永远不会 Done
            fmt.Println("context done", ctx.Err())
            return
        default:
            chunk, err := srs[1].Recv()
            if err != nil {
                if errors.Is(err, io.EOF) {
                    break outer
                }
            }
            fullMsgs = append(fullMsgs, chunk)
        }
    }
}()
```

**问题**：
- `ctx` 是 `context.Background()`（第 202 行赋值），`ctx.Done()` 永远不会触发
- 即使修复 F03-1 传递正确 ctx，此 goroutine 在后台运行，HTTP ctx 可能在 goroutine 完成前取消
- 需要独立的 context 策略：goroutine 应使用 `context.WithoutCancel`（Go 1.21+）或独立 Background 来确保保存完成
- `srs[1].Recv()` 遇到非 EOF 错误时，仍 `append(fullMsgs, chunk)` — chunk 可能是零值
- `defer` 中 `SaveMessage` 使用已被替换的 ctx 记日志

### F03-3：defaultChatModelFactory.Create 使用 context.Background()

**文件**：`server/internal/logic/chat/chat.go`
**函数**：`defaultChatModelFactory.Create`
**行号**：38-44

```go
func (f *defaultChatModelFactory) Create(apiKey, baseURL, modelName string) (model.BaseChatModel, error) {
    return openai.NewChatModel(context.Background(), &openai.ChatModelConfig{
```

**问题**：初始化时使用 `context.Background()` 创建 OpenAI 客户端。这是初始化阶段，影响较小，但应改为传入 ctx。

### F04-1：SteamResponse 错误时仍发送 DONE

**文件**：`server/core/common/stream.go`
**函数**：`SteamResponse`
**行号**：44-64

```go
for {
    chunk, err := streamReader.Recv()
    if err == io.EOF {
        break              // 正常 EOF → break 到 writeSSEDone ✓
    }
    if err != nil {
        writeSSEError(httpResp, err)
        break              // 错误 → break 到 writeSSEDone ✗
    }
    // ...
}
// 发送结束事件
writeSSEDone(httpResp)     // ← ★ F04-1: 无论正常/错误都发送 DONE
return nil                 // ← ★ F04-1: 总是返回 nil
```

**问题**：
- 流读取错误时，先发送 `event: error`，然后 break 循环，仍然发送 `[DONE]`
- 函数总是返回 `nil`，调用方无法区分成功和失败
- 客户端收到 `[DONE]` 后认为流正常结束，无法感知错误

### F04-2：SSE 写入不检查错误

**文件**：`server/core/common/stream.go`
**函数**：`writeSSEData`、`writeSSEDone`、`writeSSEError`、`writeSSEDocuments`
**行号**：68-92

```go
func writeSSEData(resp *ghttp.Response, data string) {
    resp.Writeln(fmt.Sprintf("data:%s\n", data))
    resp.Flush()
}
func writeSSEDone(resp *ghttp.Response) {
    resp.Writeln(fmt.Sprintf("data:%s\n", "[DONE]"))
    resp.Flush()
}
```

**问题**：
- `Writeln` 和 `Flush` 的返回错误被忽略
- 客户端断开后，写入失败但不检测，继续循环发送
- 浪费资源持续向已断开的连接写入

### F04-3：writeSSEError 使用 context.Background() 记日志

**文件**：`server/core/common/stream.go`
**函数**：`writeSSEError`
**行号**：88-91

```go
func writeSSEError(resp *ghttp.Response, err error) {
    g.Log().Error(context.Background(), err)
```

**问题**：使用 `context.Background()` 记日志，丢失请求关联。应传入 HTTP ctx。

### F04-4：Controller ChatStream 建流失败返回空成功

**文件**：`server/internal/controller/rag/rag_v1_chat_stream.go`
**函数**：`ChatStream`
**行号**：30-34

```go
streamReader, err = chatI.GetAnswerStream(ctx, req.ConvID, retriever.Document, req.Question)
if err != nil {
    g.Log().Error(ctx, err)
    return &v1.ChatStreamRes{}, nil   // ← ★ F04-4: 建流失败返回空成功，err 为 nil
}
```

**问题**：
- 获取流失败时，记录日志但返回 `&v1.ChatStreamRes{}, nil`
- 客户端收到成功响应但无内容，无法区分"空回答"和"建流失败"
- 应返回 error 让框架处理

### F04-5：后台 goroutine 非 EOF 错误时仍 append 零值 chunk

**文件**：`server/internal/logic/chat/chat.go`
**函数**：`GetAnswerStream`
**行号**：231-237

```go
chunk, err := srs[1].Recv()
if err != nil {
    if errors.Is(err, io.EOF) {
        break outer
    }
}
fullMsgs = append(fullMsgs, chunk)  // ← ★ F04-5: 非 EOF 错误时 chunk 可能是零值
```

**问题**：
- 非 EOF 错误时，不 break 也不 return，继续 append 可能是零值的 chunk
- 后续 `ConcatMessages` 可能包含无效数据

---

## 行为表

| 场景 | 当前行为 | 期望行为 |
|---|---|---|
| 正常完成（EOF） | 发送 DONE，返回 nil | 发送 DONE，返回 nil ✅ 不变 |
| 模型 Stream 失败 | 返回 error 给 controller，controller 返回空成功 | 返回 error，controller 传递 error 给客户端 |
| 流读取中模型错误 | 发送 error 事件 + DONE，返回 nil | 发送 error 事件，**不发送 DONE**，返回 error |
| 用户取消（客户端断开） | ctx 被替换为 Background，模型/协程继续 | 识别 context.Canceled，取消模型调用，停止后台 goroutine |
| 超时 | 无超时传递到模型 | 传递 ctx deadline 到模型，识别 DeadlineExceeded |
| SSE 写入失败 | 忽略写入错误，继续发送 | 检测写入失败，停止发送，返回 error |
| 后台保存消息失败 | 仅记日志 | 记日志（保持，后台保存失败不应影响用户响应） |
| 建流失败 | 返回空成功 `&ChatStreamRes{}, nil` | 返回 error |

---

## 批次划分

推荐拆分为 **1C-1** 和 **1C-2** 两个批次。

### 首批推荐：1C-1（F04 SSE 流语义修复）

**理由**：
- 修改范围集中在 `stream.go` 和 `rag_v1_chat_stream.go`
- 不涉及 goroutine 协调复杂性
- 测试简单（mock StreamReader + httptest.ResponseRecorder）
- 行为变化明确：错误时不发 DONE、返回 error
- 为 1C-2 的 context 修复提供正确的 SSE 层基础

### 第二批：1C-2（F03 Chat context 修复）

**理由**：
- 涉及 `chat.go` 的 context 传递和后台 goroutine 生命周期
- 需要仔细处理后台保存 goroutine 的 context 策略
- 依赖 1C-1 的 SSE 错误语义（后台 goroutine 的错误处理需要与 SSE 层一致）
- 需要可注入的模型工厂来测试 context 传递

---

## 1C-1：F04 SSE 流语义修复

### 修改文件

| 文件 | 变更 |
|---|---|
| `server/core/common/stream.go` | SteamResponse 区分 EOF/错误；错误时不发 DONE；返回 error；writeSSE* 函数传入 ctx 并检查写入错误 |
| `server/internal/controller/rag/rag_v1_chat_stream.go` | 建流失败返回 error 而非空成功 |
| `server/core/common/stream_test.go` | 新增测试 |

### 修改内容

#### 1. SteamResponse 区分正常/错误结束

```go
func SteamResponse(ctx context.Context, streamReader *schema.StreamReader[*schema.Message], docs []*schema.Document) (err error) {
    // ... 设置响应头 ...
    
    for {
        chunk, err := streamReader.Recv()
        if err == io.EOF {
            writeSSEDone(httpResp)  // 正常结束才发 DONE
            return nil
        }
        if err != nil {
            writeSSEError(ctx, httpResp, err)  // 传入 ctx
            return fmt.Errorf("stream recv failed: %w", err)  // 返回 error，不发 DONE
        }
        // ... 发送 chunk ...
        if writeErr := writeSSEData(ctx, httpResp, string(marshal)); writeErr != nil {
            return fmt.Errorf("stream write failed: %w", writeErr)
        }
    }
}
```

#### 2. writeSSE* 函数传入 ctx 并检查写入错误

```go
func writeSSEData(ctx context.Context, resp *ghttp.Response, data string) error {
    if len(data) == 0 {
        return nil
    }
    _, err := resp.Writeln(fmt.Sprintf("data:%s\n", data))
    if err != nil {
        return err
    }
    return resp.Flush()
}

func writeSSEError(ctx context.Context, resp *ghttp.Response, err error) {
    g.Log().Error(ctx, err)  // 使用请求 ctx 而非 Background
    resp.Writeln(fmt.Sprintf("event: error\ndata: %s\n\n", err.Error()))
    resp.Flush()
}
```

#### 3. Controller 建流失败返回 error

```go
streamReader, err = chatI.GetAnswerStream(ctx, req.ConvID, retriever.Document, req.Question)
if err != nil {
    return nil, err  // 传递 error 而非返回空成功
}
```

### 测试

```go
// stream_test.go

func TestSteamResponse_NormalEOF(t *testing.T) {
    // mock StreamReader 返回几个 chunk 然后 EOF
    // 验证：发送了数据事件 + DONE，返回 nil
}

func TestSteamResponse_StreamError_NoDone(t *testing.T) {
    // mock StreamReader 返回一个 chunk 后返回非 EOF 错误
    // 验证：发送了数据事件 + error 事件，未发送 DONE，返回 error
}

func TestSteamResponse_WriteError(t *testing.T) {
    // 使用已关闭的连接或 mock 写入失败
    // 验证：返回 error
}

func TestSteamResponse_EmptyStream(t *testing.T) {
    // mock StreamReader 立即返回 EOF
    // 验证：只发送 DONE，返回 nil
}

func TestSteamResponse_ContextCancel(t *testing.T) {
    // 在读取过程中取消 ctx
    // 验证：行为正确（当前层不直接处理 ctx cancel，由 Recv 返回错误触发）
}
```

### 验证命令

```
go build ./...
go vet ./...
go test ./core/common/... -count=1 -v
go test -race ./core/common/... -count=1
```

### 回滚边界

修改涉及 `stream.go` 函数签名变化（writeSSE* 新增 ctx 参数），回滚需同时恢复调用方。

---

## 1C-2：F03 Chat context 修复

### 修改文件

| 文件 | 变更 |
|---|---|
| `server/internal/logic/chat/chat.go` | 移除 `ctx = context.Background()`；后台保存 goroutine 使用独立 context 策略；修复非 EOF 错误时 append 零值 chunk |
| `server/internal/logic/chat/chat_test.go` | 新增 context 传递测试 |

### 修改内容

#### 1. 移除 context.Background() 替换

```go
func (x *Chat) GetAnswerStream(ctx context.Context, convID string, docs []*schema.Document, question string) (...) {
    messages, err := x.docsMessages(ctx, convID, docs, question)
    if err != nil {
        return
    }
    // 使用传入的 ctx 调用模型，客户端断开时模型调用可取消
    streamData, err := stream(ctx, x.cm, messages)
```

#### 2. 后台保存 goroutine 使用 context.WithoutCancel

后台保存 goroutine 需要确保即使 HTTP ctx 取消，保存操作仍能完成（用户已收到数据，消息应持久化）。使用 `context.WithoutCancel(parentCtx)` 派生独立 ctx：

```go
// Go 1.21+ 提供 context.WithoutCancel
bgCtx := context.WithoutCancel(ctx)

go func() {
    fullMsgs := make([]*schema.Message, 0)
    defer func() {
        srs[1].Close()
        fullMsg, concatErr := schema.ConcatMessages(fullMsgs)
        if concatErr != nil {
            g.Log().Error(bgCtx, "error concatenating messages: %v", concatErr)
            return
        }
        save_err := x.eh.SaveMessage(fullMsg, convID)
        if save_err != nil {
            g.Log().Error(bgCtx, "save assistant message err: %v", save_err)
        }
    }()
    // ...
}()
```

**注意**：如果 Go 版本不支持 `context.WithoutCancel`（需要 Go 1.21+），可使用 `context.Background()` 但明确注释意图：

```go
// 后台保存使用独立 context，不随 HTTP 请求取消
// 确保用户已收到的消息能持久化保存
bgCtx := context.Background() // intentional: save must complete even if request is cancelled
```

#### 3. 修复非 EOF 错误时 append 零值 chunk

```go
chunk, err := srs[1].Recv()
if err != nil {
    if errors.Is(err, io.EOF) {
        break outer
    }
    // 非 EOF 错误：记录日志，停止消费但不保存不完整消息
    g.Log().Error(bgCtx, "stream recv error: %v", err)
    break outer
}
fullMsgs = append(fullMsgs, chunk)  // 只在无错误时 append
```

#### 4. defaultChatModelFactory.Create 传入 ctx

```go
type ChatModelFactory interface {
    Create(ctx context.Context, apiKey, baseURL, modelName string) (model.BaseChatModel, error)
}

func (f *defaultChatModelFactory) Create(ctx context.Context, apiKey, baseURL, modelName string) (model.BaseChatModel, error) {
    return openai.NewChatModel(ctx, &openai.ChatModelConfig{
```

**注意**：这改变了 `ChatModelFactory` 接口签名，需要同步更新测试中的 fake。

### 测试

```go
// chat_test.go

func TestGetAnswerStream_ContextCancel(t *testing.T) {
    // 注入 fake model，Stream 方法等待 ctx 取消
    // 取消 ctx 后验证：stream 返回 context.Canceled 错误
}

func TestGetAnswerStream_ContextPassedToModel(t *testing.T) {
    // 注入 fake model，验证 Stream 收到的 ctx 不是 Background
    // 取消父 ctx 后 fake 应观察到 ctx.Done()
}

func TestGetAnswerStream_BackgroundSave_CompletesAfterCancel(t *testing.T) {
    // 验证即使 HTTP ctx 取消，后台保存 goroutine 仍完成消息保存
    // 使用注入的 fake history 验证 SaveMessage 被调用
}

func TestGetAnswerStream_NonEOFError_NoPanic(t *testing.T) {
    // 验证非 EOF 错误时不 append 零值 chunk
    // 后台 goroutine 正常退出不 panic
}
```

### 验证命令

```
go build ./...
go vet ./...
go test ./internal/logic/chat/... -count=1 -v
go test -race ./internal/logic/chat/... -count=1
```

### 回滚边界

修改涉及 `ChatModelFactory` 接口签名变化，回滚需同时恢复 fake 实现。

---

## 接口影响汇总

| 批次 | 接口变化 | 影响范围 |
|---|---|---|
| 1C-1 | `writeSSEData`/`writeSSEError`/`writeSSEDone`/`writeSSEDocuments` 新增 ctx 参数 | 仅 `stream.go` 内部函数，无外部 API 变化 |
| 1C-1 | `SteamResponse` 返回值语义变化（错误时返回 error 而非 nil） | `rag_v1_chat_stream.go` 调用方 |
| 1C-2 | `ChatModelFactory.Create` 新增 ctx 参数 | `chat.go` 和测试 fake |
| 1C-2 | `GetAnswerStream` 行为变化（context 正确传递） | 无接口签名变化 |

---

## 离线依赖注入和测试方案

### 1C-1 测试

- **mock StreamReader**：创建 `schema.StreamReader[*schema.Message]` 的 fake 实现，控制返回的 chunk 和错误
- **httptest**：使用 GoFrame 测试框架或模拟 HTTP response 验证 SSE 输出
- 不连接真实模型

### 1C-2 测试

- **fake BaseChatModel**：已有 `ChatModelFactory` 接口注入机制，使用 fake model 验证 ctx 传递
- **fake historyWrapper**：验证后台保存调用
- **context 验证**：使用 `context.WithCancel` 和 channel 信号验证 ctx 正确传递
- 不连接真实模型或数据库

### 测试执行保证

- 所有测试默认运行，无需 build tag
- 不触发数据库/向量服务/OpenAI 连接
- race detector 覆盖

---

## 验证命令与退出条件

### 每批完成后

```
go build ./...                                          # 退出码 0
go vet ./...                                            # 退出码 0
go test ./core/common/... -count=1 -v                   # 退出码 0（1C-1）
go test ./internal/logic/chat/... -count=1 -v           # 退出码 0（1C-2）
go test -race ./core/common/... -count=1                # 退出码 0（1C-1）
go test -race ./internal/logic/chat/... -count=1        # 退出码 0（1C-2）
```

### 全量验证

```
go test -p 1 ./... -count=1                             # 退出码 0
```

### 退出条件

- 所有新增测试通过
- 已有测试不受影响（或同步更新）
- `go build`/`go vet` 退出码 0
- race detector 通过
- 离线测试不触发真实连接

---

## Stage 1D/Stage 5 等后续事项的边界

### Stage 1C 不处理

| 事项 | 归属 |
|---|---|
| Chunk 写入错误传播（F09） | Stage 1D |
| QA 并发控制（F07/F08） | Stage 1D |
| SSE 协议标准化（v2 接口） | Stage 5 |
| chat-history 替换 | Stage 5 |
| 持久化任务恢复 | Stage 4 |
| 模型超时配置化 | Stage 2 |
| OpenTelemetry 链路追踪 | Stage E |
| 完整 context 超时策略（含检索+模型+保存） | Stage 5 |

### Stage 1C 边界说明

- 仅修复 F03/F04 已识别的 context 丢失和 SSE 错误语义
- 不实现完整的 SSE v2 协议
- 不实现消息持久化状态机
- 不处理非流式 Chat 接口的 context 问题（`GetAnswer` 已正确传递 ctx）
- 后台保存 goroutine 仅修复 context 策略，不实现持久化保证

---

## 依赖关系

```
1A (init 清理) ──┐
                  │
1B (并发+rerank) ─┤
                  │
                  ▼
            1C-1 (F04 SSE) ← 首批推荐
                  │
                  ▼
            1C-2 (F03 context)
                  │
                  ▼
            1D (F07/F08/F09)
```

1C-1 和 1C-2 有轻微依赖：1C-2 的后台 goroutine 错误处理受益于 1C-1 的 SSE 错误语义。建议按顺序执行。

---

## 实施顺序

1. **1C-1（F04 SSE 流语义）** — ✅ 推荐首批
2. **1C-2（F03 Chat context）** — 第二批

