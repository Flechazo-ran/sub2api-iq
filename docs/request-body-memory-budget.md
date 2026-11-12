# 请求体内存总额度

对应 issue #16。照下游 CN 站（lumai-beijing/sub2api，主体来自上游 Wei-Shaw/sub2api）移植，按「兜底服务、宁可放行」的原则做了保守化。

## 做什么

- 整个实例只有一个额度，所有网关路由共用。
- 请求进网关时由 `RequestBodyLimit` 中间件挂上预占记录；读请求体时，每一份缓存（读取分块、拼好的整块、解压结果、JSON 规范化副本、zstd 解码窗口）在分配前先扣额度，中间产物用完马上退，最终请求体一直扣到 handler 返回。
- 转发时改写出来的请求体副本按每次尝试计，失败换号时不累积，`Forward` 结束时归还。
- 异步图片任务在请求返回后还要用请求体，额度转给任务自己，任务结束（`run` 里的 `defer cancel()`）时归还。
- 额度用满时，读请求体的地方返回 503 `Request body buffering capacity is exhausted; retry later`，带 `Retry-After: 1`。按入口协议给 OpenAI（`type: server_error`）、Anthropic（`type: api_error`）或 Gemini 格式。单个请求超过大小上限仍是 413，两者分开。

## 什么时候拒绝、什么时候放行

只有一种情况会拒绝：**新请求读请求体时额度确实不够**。其余算不准或已经接下的情况一律放行：

| 情况 | 处理 |
|---|---|
| 新请求读请求体，额度不够 | 503，客户端重试 |
| 转发时的改写副本，额度不够 | 不记账，照常转发 |
| 异步图片任务，额度不够 | 不记账，照常执行 |
| handler 已返回、后台还在用同一个 ctx | 不记账，不拒绝 |
| 读请求体时 ctx 已取消 | 照常读，交给 handler 判断（和没有额度时一样） |
| WebSocket 帧 | 不计入（和 CN 一致） |
| 不经过公共读体函数的读取（如 `grok_audio.go`） | 不计入 |

## 额度大小

- 配置项 `gateway.request_body_memory_budget_bytes`，环境变量 `GATEWAY_REQUEST_BODY_MEMORY_BUDGET_BYTES`。
- 不配置时，启动时自动取「物理内存、cgroup 限额、`GOMEMLIMIT`」三者最小值的 1/4，最低 1GiB，并打一条 `gateway.request_body_memory_budget_bytes auto-sized from memory limit` 日志。
- 显式配置原样使用；**配 0 关闭额度**：不记账、不返回 503，运维页也不显示这一行（紧急回退用这个，改环境变量后重建容器生效）。
- 147：容器 8GiB，设 `GOMEMLIMIT=4000MiB`（11-12 定）后 1/4 是 1000MiB，按下限取 1GiB；不设 `GOMEMLIMIT` 时是 2GiB。
- 单个请求的大小上限不变：`gateway.max_body_size` 和 `server.max_request_body_size` 都是 256MB。

## 怎么看用量

- 运维监控页「内存」卡片下多一行「请求体缓冲」：上一个采集窗口的峰值 / 额度上限；窗口内有 503 时标红并显示次数。
- 数据在 `ops_system_metrics` 的 `request_body_budget_limit_mb`、`request_body_budget_peak_mb`、`request_body_budget_rejected` 三列（迁移 312）。
- 拒绝时打 `request_body_memory_budget_exhausted` 警告日志，最多每 10 秒一条，带路径、已用、上限。

## 顺带修复（#10）

- gzip / deflate / zstd 解压后超过 64MB 时返回 413，不再静默截断成残缺 JSON。
- zstd 解码器内存上限设为 64MB（原来按帧头声明的窗口分配，没有上限）。

## 边界

- 额度只管请求体缓存，不是整个进程内存的硬上限。转发时把 JSON 解析成对象、SSE 缓冲、各种缓存都不在额度内。防内存失控要和 `GOMEMLIMIT`、容器内存上限（不开 swap）一起用。
- 额度全站共享：一个分组的大请求多了，别的分组也会收到 503。
- 上游卡住、请求不结束时，额度会被一直占着（CN 11-12 事故，lumai-beijing/sub2api#1587）。建议同时配置首输出超时 `gateway.openai_first_output_timeout_seconds`。
