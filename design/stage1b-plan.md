# Stage 1B 细化实施计划

## 概述

Stage 1B 目标：修复 Retriever 并发、Indexer 异步闭包和 Rerank HTTP 错误处理问题。

**范围**：
- F01：Retriever.Retrieve 并发安全
- F02：Indexer.Index 异步闭包错误处理
- F05：Rerank HTTP 请求绑定 ctx

**不修改**：依赖版本、数据库结构、ES 镜像。
**不调用**：真实模型或外部服务。

**前置条件**：Stage 1A 已完成（确保测试不触发真实连接）

**基线**：`db0c5971d137ddabcc10ae5fb0c4b9baa17e1b98`（Stage 1-Pre 完成后的 HEAD）

## 批次划分

推荐拆分为 1B-1/1B-2/1B-3，最小首批为 **1B-1（F05 Rerank HTTP）**，因为：
- 修改范围最小（单文件）
- 无并发复杂性
- 测试简单（httptest）
- 可独立验证

**1B-1 已完成**：见 `CHANGES.md` 和 `PROJECT_PROGRESS.md` Stage 1B-1 记录。

---

## 1B-1：F05 Rerank HTTP 修复

### 问题描述

`server/core/rerank/rerank.go` 第 109 行：
- HTTP 请求使用 `context.Background()`，未绑定传入 ctx
- 无 timeout 限制
- 无响应体大小限制
- 无状态码和 JSON 校验
- 无验证结果下标范围、重复下标及非法分数

### 修改文件

- `server/core/rerank/rerank.go`

### 修改内容

1. **HTTP 请求绑定 ctx**：
   ```go
   // 修改前
   req, err := http.NewRequest("POST", url, bytes.NewReader(body))
   
   // 修改后
   req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
   ```

2. **有界 timeout**：
   ```go
   ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
   defer cancel()
   ```

3. **响应体大小限制**：
   ```go
   resp.Body = http.MaxBytesReader(nil, resp.Body, 10<<20) // 10MB
   ```

4. **状态码和 JSON 校验**：
   ```go
   if resp.StatusCode != http.StatusOK {
       return nil, fmt.Errorf("rerank API returned %d", resp.StatusCode)
   }
   ```

5. **验证结果**：
   ```go
   for _, result := range results {
       if result.Index < 0 || result.Index >= len(docs) {
           return nil, fmt.Errorf("invalid index %d", result.Index)
       }
       if result.RelevanceScore < 0 || result.RelevanceScore > 1 {
           return nil, fmt.Errorf("invalid score %f", result.RelevanceScore)
       }
   }
   // 检查重复下标
   seen := make(map[int]bool)
   for _, result := range results {
       if seen[result.Index] {
           return nil, fmt.Errorf("duplicate index %d", result.Index)
       }
       seen[result.Index] = true
   }
   ```

6. **错误返回与降级策略**：
   - Rerank 失败返回错误，不降级（Rerank 是必需步骤）
   - 调用方可决定是否降级

### 测试

创建 `server/core/rerank/rerank_test.go`（或扩展已有）：

```go
func TestRerank_ContextCancel(t *testing.T) {
    ctx, cancel := context.WithCancel(context.Background())
    cancel() // 立即取消
    
    _, err := Rerank(ctx, "query", docs)
    if err == nil { t.Fatal("expected error") }
}

func TestRerank_Timeout(t *testing.T) {
    // 使用 httptest 模拟慢响应
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        time.Sleep(2 * time.Second)
        w.WriteHeader(http.StatusOK)
    }))
    defer server.Close()
    
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()
    
    _, err := Rerank(ctx, "query", docs)
    if err == nil { t.Fatal("expected timeout error") }
}

func TestRerank_500Error(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
    }))
    defer server.Close()
    
    _, err := Rerank(context.Background(), "query", docs)
    if err == nil { t.Fatal("expected error") }
}

func TestRerank_BadJSON(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Write([]byte("not json"))
    }))
    defer server.Close()
    
    _, err := Rerank(context.Background(), "query", docs)
    if err == nil { t.Fatal("expected error") }
}

func TestRerank_InvalidIndex(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        json.NewEncoder(w).Encode([]RerankResult{Index: 999, RelevanceScore: 0.5})
    }))
    defer server.Close()
    
    _, err := Rerank(context.Background(), "query", docs)
    if err == nil { t.Fatal("expected error for invalid index") }
}

func TestRerank_DuplicateIndex(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        json.NewEncoder(w).Encode([]RerankResult{
            {Index: 0, RelevanceScore: 0.5},
            {Index: 0, RelevanceScore: 0.3},
        })
    }))
    defer server.Close()
    
    _, err := Rerank(context.Background(), "query", docs)
    if err == nil { t.Fatal("expected error for duplicate index") }
}
```

### 验证命令

```
go test ./core/rerank/... -count=1 -v
go test -race ./core/rerank/... -count=1
```

### 回滚边界

单文件修改，回滚简单。

---

## 1B-2：F01 Retriever 并发修复

### 问题描述

`server/core/retriever.go` 第 76 行：
- 循环修改 `req.optQuery`，多 goroutine 共享
- 第 81 行：`err` 命名返回值竞争

### 修改文件

- `server/core/retriever.go`

### 修改内容

1. **每轮查询构造独立请求**：
   ```go
   for i, query := range queries {
       // 构造独立请求，深拷贝 slice/map
       reqCopy := *req
       reqCopy.optQuery = make([]string, len(req.optQuery))
       copy(reqCopy.optQuery, req.optQuery)
       reqCopy.optQuery = append(reqCopy.optQuery, query)
       
       go func(r *RetrieveReq) {
           // goroutine 使用局部错误变量
           var localErr error
           // ...
       }(&reqCopy)
   }
   ```

2. **goroutine 使用局部错误变量**：
   ```go
   go func(r *RetrieveReq) {
       var localErr error
       result, localErr := retriever.Retrieve(ctx, r)
       if localErr != nil {
           errCh <- localErr
           return
       }
       resultCh <- result
   }(&reqCopy)
   ```

3. **查询与结果正确绑定**：
   - 使用带索引的 channel 或结构体
   - 相同 Chunk 去重、高分选择

4. **区分必需检索失败与可选检索降级**：
   - 主检索器失败：返回错误
   - 辅助检索器失败：记录日志，继续

### 测试

```go
func TestRetrieve_ConcurrentQueries(t *testing.T) {
    // 用不同查询返回不同证据
    // 覆盖同 Chunk 不同分数
    // 重复运行 race 检测
}

func TestRetrieve_ChunkDedup(t *testing.T) {
    // 验证相同 Chunk 去重
}

func TestRetrieve_ChunkHighScore(t *testing.T) {
    // 验证相同 Chunk 保留高分
}
```

### 验证命令

```
go test ./core -count=1 -v
go test -race ./core -count=1
```

### 回滚边界

单文件修改，回滚简单。

---

## 1B-3：F02 Indexer 异步闭包修复

### 问题描述

`server/core/indexer.go` 第 63 行：
- 异步闭包写外部命名返回值 `err`

### 修改文件

- `server/core/indexer.go`

### 修改内容

1. **异步闭包不写外部返回变量**：
   ```go
   // 修改前
   go func() {
       err = doAsyncWork() // 写外部变量
   }()
   
   // 修改后
   errCh := make(chan error, 1)
   go func() {
       errCh <- doAsyncWork()
   }()
   ```

2. **明确哪些错误同步返回、哪些异步**：
   - 同步错误：参数校验、配置读取
   - 异步错误：后台任务失败，记录状态

3. **不宣称已有持久化恢复能力**：
   - 异步任务失败记录日志
   - 不宣称可重启恢复

### 测试

```go
func TestIndex_AsyncError(t *testing.T) {
    // 用 fake 和同步屏障触发失败
    // 不以固定 sleep 代替验证
}
```

### 验证命令

```
go test ./core -count=1 -v
go test -race ./core -count=1
```

### 回滚边界

单文件修改，回滚简单。

---

## 实施顺序

1. **1B-1（F05 Rerank）** — 最小首批，单文件，测试简单
2. **1B-2（F01 Retriever）** — 并发修复，需仔细测试
3. **1B-3（F02 Indexer）** — 异步闭包，需明确错误模型

## 全局验证命令

```
go build ./...
go vet ./...
go test -p 1 ./... -count=1
go test -race ./core/... -count=1
```

## 回滚说明

每个修复独立可回滚。
