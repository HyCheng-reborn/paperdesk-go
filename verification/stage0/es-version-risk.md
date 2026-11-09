# ES 版本风险记录

## 版本对比

| 来源 | 版本 | 位置 |
|---|---|---|
| go-elasticsearch 客户端 | v8.16.0 | server/go.mod:23 |
| docker-compose ES 镜像 | 8.11.3 | docker-compose.yml:31 |
| README 安装示例 | 8.18.0 | README.md:86 |

## 兼容性分析

- go-elasticsearch v8.16.0 客户端官方保证兼容 ES 服务端 >= 8.16
- docker-compose 中 ES 服务端 8.11.3 低于客户端版本，不在官方保证区间
- README 示例 8.18.0 与 Compose 8.11.3 不同，增加环境不一致风险

## 本轮决策

- 不升级 ES
- 制定独立基础设施验证计划
- 验证须覆盖：mapping、写入、BM25、kNN、删除、搜索可见性
- 不以 ping 成功替代兼容性验证
- 该风险不阻塞 Stage 1 离线逻辑修复

## 待验证项

- Stage 1 启动验证时确认实际连通性
- 或将 ES 镜像升级到 >= 8.16
- 统一三方版本（客户端、Compose、README）
