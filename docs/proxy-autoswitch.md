# 代理优选（自动换代理）

入口：**线路优化 → 代理优选**（/admin/proxy-autoswitch）。默认关闭。

代理优选每分钟看一遍指定分组里的每个号：这个号在当前代理上最近几分钟的使用记录首字够慢（首字 p60 和首字均值都超过阈值），就从人工勾选的候选代理池里按策略挑一个代理换上。每次换代理都留一条记录，写明换前的数据、两个代理的测速，以及换后 10 分钟的数据，方便对比换了有没有用。

本文按 v2（PR #13，2026-11-06）的代码写。v2 是重做的版本，以前的连续几轮、观察期、冷却、每小时上限、换回都已经去掉，见「v2 没有的东西」和「版本变化」。

## 每分钟一轮做什么

后台任务每分钟跑一轮，单轮最长 4 分钟。多实例部署时用领导锁保证同一时间只有一个实例在跑；`runtime.role` 是 gateway 的实例不启动这个任务。

每一轮按顺序做：

1. 先把所有「统计中」（`measuring`）的记录往前推进：补上换后的数据，满 10 分钟的定稿。**功能关着也会做这一步**，已经换过的记录照样收尾。
2. 配置里一个分组都没有，或开关是关的，这一轮到此结束。
3. 取指定分组里所有未删除的号，挑出参与判断的号（下表），逐个判断，判定为慢的马上去换。

参与判断的号必须同时满足：

| 条件 | 不满足时账号监控里显示 |
|---|---|
| 没删除 | 不列出 |
| 不是影子账号（影子号跟着母账号的代理走） | 账号不可用：影子账号跟随母账号的代理，不参与自动换代理 |
| OpenAI 平台、OAuth 类型 | 账号不可用：不是 OpenAI OAuth 号，不参与自动换代理 |
| 状态是 active | 账号不可用：账号状态不是 active |
| 可调度 | 账号不可用：账号不可调度 |
| 挂着代理 | 没有代理 |

## 判慢规则

### 取哪些请求

首字取的是 `usage_logs.first_token_ms`，和使用记录页、首字趋势看板是同一个数。取样规则和首字趋势共用同一段代码（`firstTokenUsageValue`），两边对同一批使用记录取到的样本完全一样。

| 规则 | 说明 |
|---|---|
| 时间窗口 | 过去 `window_minutes` 分钟（默认 5）。按请求**开始时间**算，开始时间 = 完成时间 − `duration_ms` |
| 只算流式请求 | 非流式请求、`first_token_ms` 为空的请求不算 |
| 只算已完成的请求 | 使用记录在请求结束时才写，还在跑的请求不算 |
| 只算指定分组下的请求 | 按 `usage_logs.group_id` 取，号在别的分组下的请求不算 |
| 只算当前代理上的请求 | 用 request_id 去连接 trace（`request_connection_traces`）里对出这条请求走的代理，对出来是别的代理的不算；对不上 trace 的按号现在的代理算 |

### 怎么判

| 步骤 | 规则 |
|---|---|
| 1 | 请求数 < `min_samples`（默认 20）：状态 `insufficient`，不判断，不换 |
| 2 | 首字 p60 > `p60_threshold_ms`（默认 10 秒）**且** 首字均值 > `mean_threshold_ms`（默认 15 秒）：状态 `slow`，这一轮马上换 |
| 3 | 其他情况：状态 `ok`，不换 |

- 两个阈值必须**同时**超过，只超一个不换。刚好等于阈值不算超。
- p60 的算法和首字趋势的 p50 一样（PostgreSQL `percentile_cont` 式线性插值），分位换成 0.6，四舍五入到毫秒。均值是算术平均，四舍五入到毫秒。
- 耗时均值 = 每个请求的完成时间 − 开始时间，取平均。只用来显示，不参与判断。

## 选代理

### 候选池

候选池是配置里的 `candidate_proxy_ids`，人工从代理列表里勾选，所有指定分组共用一个池子。换代理时只从池子里挑，池子里的代理要同时满足下面几条才算候选：

| 条件 | 说明 |
|---|---|
| 没删除 | 已删除的代理不算 |
| 状态是 active | 停用的不算 |
| 没过期 | `expires_at` 为空，或晚于现在 |
| 没有别的号在用 | 只要还有一个未删除的号挂着这个代理就不算（包括不在指定分组里的号） |
| 不是这个号现在的代理 | 当前代理就在池子里时会跳过它 |

### 选择策略

| 策略 | 值 | 怎么挑 |
|---|---|---|
| 随机 | `random`（默认） | 在候选里随机挑一个 |
| 顺序 | `sequential` | 按池子的顺序（页面上可以上移、下移），从当前代理在池子里的位置往后找第一个候选，到末尾再从头找；当前代理不在池子里就从头找 |
| 不同地区 | `region` | 在候选里挑地区和当前代理不同的，随机一个。地区未知的候选跳过；一个都没有就算没候选 |

地区是代理的出口国家代码：先读代理测速缓存和本进程的缓存（6 小时），缺的再对代理做一次出口探测（单轮最多 30 秒；探测失败 30 分钟内不再测）。

### 没候选时

没挑到代理时写一条 `no_candidate` 记录，`reason` 写原因，不改号的代理。这个号上一条记录已经是同一个代理、同一个原因的 `no_candidate` 时不重复写，所以号一直慢、池子一直空的话只会有一条记录。

| reason | 什么情况 |
|---|---|
| 候选代理池为空 | 池子里一个代理都没勾 |
| 池子里的代理都被别的号占用了 | 池子里的代理都有号在用 |
| 池子里的代理都过期了 | 都过期 |
| 池子里的代理都不可用了 | 都不是 active |
| 候选代理池里只有当前代理 | 池子里只有这个号自己的代理 |
| 没有可用候选代理：N 个不存在或已删除，N 个已过期…… | 几种原因混在一起，逐项列个数 |
| 顺序策略没有可用候选代理 | 顺序策略没找到（正常不会出现） |
| 没有不同地区的候选代理 | 不同地区策略下，候选都和当前代理同地区或地区未知 |
| 候选代理在换之前被别的号占用了 | 选好之后、真正改之前，目标代理被别的号用上了 |

## 换代理

1. 测速：对旧代理、新代理各测一次。每次测速是通过代理新建 3 次连接，请求 OpenAI 网站的一个公开地址，计到收到响应头的时间，取中位数；单次 8 秒超时。不带账号凭证，不耗额度。3 次都失败记 `null`。**测速结果只记进记录，不影响换不换。**
2. 加锁复查：拿到换代理锁（进程内互斥 + 数据库 advisory lock，最多等 5 秒）后再查一次：
   - 号已经不在旧代理上（被人工改了、被删了）：放弃这次，不写记录，只打日志 `proxy_autoswitch_account_changed_before_switch`。
   - 目标代理已经有号在用：写 `no_candidate`，原因「候选代理在换之前被别的号占用了」。
3. 先写一条 `measuring` 记录，再改号的代理。改失败把这条记录改成 `failed`，`reason` 写失败原因。
4. 改代理**只改 `accounts.proxy_id`**，走后台批量编辑的部分更新（`BulkUpdateAccounts`），会刷新调度快照，并同步影子账号的代理。

为什么不用单个账号编辑（`UpdateAccount`）：它会按几毫秒前读到的快照把整行重写，凭证也一起写回。如果这期间刚好有并发的 token 刷新，新 token 会被写回成旧值。批量编辑只改传了的字段，不碰凭证。

同一轮里多个号都慢时，逐个处理；前一个号用掉的代理，后一个号不会再选到。换代理不影响正在跑的请求：HTTP 连接池按「账号 + 代理」分开，旧连接照常读完。

## 换代理记录

每条记录里有：

| 内容 | 说明 |
|---|---|
| 换前（`before`） | 换的那一刻用来判断的那份数据：过去 `window_minutes` 分钟、旧代理上的请求数、首字 p60、首字均值、耗时均值 |
| 换后（`after`） | 换完后 **10 分钟**（固定，不能配置）这个号在新代理上的同样四个数。口径和换前一样，只看新代理上的请求 |
| 测速 | `from_probe_ms` 旧代理、`to_probe_ms` 新代理，测不通为 `null` |
| 其他 | 分组、策略、窗口分钟数、新旧代理名字和地区、原因 |

换后数据由每分钟那一轮更新，不是打开页面时实时算的，所以最多晚 1 分钟。满 10 分钟那一轮把换后数据截到 `换代理时间 + 10 分钟` 定稿，写 `after_finished_at`。
### 记录状态

| status | 页面显示 | 含义 |
|---|---|---|
| `measuring` | 统计中 | 已换，换后不满 10 分钟，`after` 是到上一轮为止的数 |
| `done` | 已完成 | 已换，换后 10 分钟统计完了 |
| `no_candidate` | 没有候选代理 | 判定为慢，但没挑到代理，没换 |
| `failed` | 换代理失败 | 挑到了代理，改账号代理时失败，没换 |
| `observing` `kept` `kept_insufficient` `not_faster` `reverted` `revert_skipped` `manual_revert` | 观察中、已换变快了等 | v1 留下的老记录，原样保留、照常返回，v2 不再产生。老记录的新字段（`before`、`after`、策略、测速等）是 `null` |

账号管理列表里，最近 7 天自动换过代理的号在代理列旁边标「自动换过」，数据来自 `/recent`。

## v2 没有的东西

下面这些是 v1 有、v2 按要求去掉的，现在的代码里都没有：

| 去掉的 | 现在是 |
|---|---|
| 连续几轮都慢才换 | 一轮判定为慢就马上换 |
| 换完观察期（观察期间不再换这个号） | 没有观察期；换后 10 分钟的统计只做记录，不拦下一次判断 |
| 同号冷却时间 | 没有冷却。号换到新代理后，只要新代理上的请求数又够了、两项又都超，下一轮就会再换 |
| 全站每小时换几次的上限 | 没有上限 |
| 没变快就换回旧代理、手动换回按钮和接口 | 没有换回 |
| 候选要比当前代理测速快多少才换、挑测速最快的 | 测速只记录，不参与挑选 |
| 离过期还剩几天才能当候选 | 只要没过期就行 |
| 从所有空闲代理里挑、优先同地区 | 只从人工勾选的候选池里挑，按三种策略 |
| 人工改过代理的号 6 小时内不动 | 没有（PR #11 已去掉） |
| 判慢口径可选（上游响应头、trace 首字） | 只有使用记录首字一种 |

## 配置项

存在 `settings` 表，key 是 `proxy_autoswitch_config`，值是 JSON。只有这 8 项：

| 字段 | 默认值 | 范围 | 含义 |
|---|---|---|---|
| `enabled` | `false` | | 开关 |
| `group_ids` | `[16]` | 分组必须存在 | 监控哪些分组。保存时去重、按 ID 升序。一个都不选就不对任何号生效。页面只列 OpenAI 分组 |
| `window_minutes` | `5` | 1–60 | 统计过去几分钟 |
| `min_samples` | `20` | 1–100000 | 窗口里至少几个请求才判断 |
| `p60_threshold_ms` | `10000` | 1–600000 | 首字 p60 大于多少毫秒（页面上按秒填） |
| `mean_threshold_ms` | `15000` | 1–600000 | 首字均值大于多少毫秒（页面上按秒填） |
| `candidate_proxy_ids` | `[]` | 代理必须存在且没删除 | 候选代理池。去重，保持给的顺序（顺序策略按这个顺序） |
| `strategy` | `random` | `random` `sequential` `region` | 选择策略 |

- 校验不过返回 400，带中文说明，例如「window_minutes 必须在 1 到 60 之间」「代理 99 不存在或已删除」。保存时只查代理存不存在，不查过期、占用，这些在换的时候才查。
- 读老配置：v1 的 `metric`、`threshold_ms`、`consecutive_checks`、`min_improvement_pct`、`account_cooldown_minutes`、`account_cooldown_hours`、`max_switches_per_hour`、`observe_minutes`、`min_days_to_expiry`、`region_policy` 一律忽略；`candidate_proxy_ids`、`p60_threshold_ms`、`mean_threshold_ms`、`strategy` 老配置里没有，用默认值。`enabled`、`group_ids`、`window_minutes`、`min_samples` 字段名没变，**沿用老配置里存的值**，不会变成 5 / 20。
- PUT 带了老字段直接忽略，不报错。
- PUT 是整份覆盖：没带的字段按零值处理（比如没带 `window_minutes` 会 400，没带 `group_ids` 会存成空）。页面每次保存都带全 8 项。
- 库里存的配置解析失败或校验不过时，GET /config 返回错误，后台每一轮也直接报错跳过（日志 `proxy_autoswitch_cycle_failed`），直到重新保存一份合法配置。

## 接口

都在 `/api/v1/admin/proxy-autoswitch` 下，需要管理员登录。

| 方法 | 路径 | 参数 | 返回的主要字段 |
|---|---|---|---|
| GET | `/config` | 无 | 上面 8 个配置字段 |
| PUT | `/config` | body 是 8 个配置字段 | 保存后的配置（去重、排序后） |
| GET | `/accounts` | 无 | `window_minutes`；`items[]`：`account_id` `account_name` `group_ids` `proxy_id` `proxy_label` `region` `samples` `p60_ms` `mean_ms` `duration_mean_ms` `state` `last_switch_at` `note` |
| GET | `/proxies` | 无 | `items[]`：`id` `name` `region` `expires_at` `expired` `account_count` `in_pool`。所有未删除的代理，池子里的排前面（按池子顺序），其余按 ID |
| GET | `/events` | `before_id`（翻页游标）、`limit`（默认 50，最大 200）、`account_id`（可选） | `items[]`、`next_cursor`（0 表示没有更多）。每条：`id` `created_at` `switched_at` `status` `account_id` `account_name` `group_id` `strategy` `reason` `from_proxy_id` `from_proxy_label` `from_region` `to_proxy_id` `to_proxy_label` `to_region` `window_minutes` `before{samples,p60_ms,mean_ms,duration_mean_ms}` `after{同上}` `from_probe_ms` `to_probe_ms` `after_finished_at`。按 ID 倒序 |
| GET | `/recent` | `days`（默认 7，最大 90） | `items[]`：每个号最近一条真正换过代理的记录，`account_id` `event_id` `at` `status` `from_proxy_label` `to_proxy_label` `reason`。账号列表的「自动换过」用它 |

v1 的 `POST /events/:id/revert`（手动换回）已删除。

账号监控 `state` 的取值：

| state | 页面显示 | 含义 |
|---|---|---|
| `unavailable` | 账号不可用 | 不是 OpenAI OAuth 号、是影子号、不是 active、不可调度，`note` 写具体原因 |
| `no_proxy` | 没有代理 | 号没挂代理 |
| `insufficient` | 不到 N 个请求 | 请求数不到 `min_samples`，`note` 写「样本 X 个，少于 N 个不判断」 |
| `slow` | 两项都超，换代理 | 下一轮会换 |
| `ok` | 正常 | 其他 |

账号监控用的是和下一轮判断一样的取样（当前代理、过去 `window_minutes` 分钟），打开页面时现算。`last_switch_at` 是这个号最近一次真正换代理的时间（含 v1 老记录）。
## 数据表

### proxy_autoswitch_events（换代理记录）

迁移 307 建表，308–311 加列、放宽 CHECK。不加外键：账号、代理删了记录照样留着（名字存了快照）。

| 字段 | 说明 |
|---|---|
| `id` `created_at` `updated_at` | |
| `account_id` | 号 |
| `status` | CHECK 同时允许 v2 的 `measuring` `done` `no_candidate` `failed` 和 v1 的老值 |
| `metric` | v2 固定写 `usage_first_token` |
| `reason` | 没候选、失败的原因，最长 300 字 |
| `group_id` `strategy` `window_minutes` | 记录当时的分组、策略、窗口（311 新增） |
| `from_proxy_id` `from_proxy_label` `from_region` | 旧代理 |
| `to_proxy_id` `to_proxy_label` `to_region` | 新代理，没换成时为空 |
| `before_samples` `before_p60_ms` `before_mean_ms` `before_duration_mean_ms` | 换前数据（p60、均值、耗时 311 新增） |
| `after_samples` `after_p60_ms` `after_mean_ms` `after_duration_mean_ms` | 换后 10 分钟数据（p60、均值、耗时 311 新增） |
| `from_probe_ms` `to_probe_ms` | 新旧代理测速（311 新增） |
| `switched_at` | 换代理的时间 |
| `after_finished_at` | 换后统计定稿的时间（311 新增） |
| `before_p50_ms` `after_p50_ms` `current_probe_ms` `candidate_probe_ms` `candidates` `finished_at` | v1 用的列，v2 不再写，老行保留 |

索引：`(account_id, id DESC)`、`(created_at DESC)`、`status = 'measuring'` 的部分索引（311）；v1 的 `status = 'observing'` 部分索引还在。

### 其他

| 表 | 用途 |
|---|---|
| `settings`（key `proxy_autoswitch_config`） | 配置 |
| `proxy_autoswitch_account_states` | v1 用来判断「人工改过代理」的表（迁移 307）。v2 不读不写，表还在 |
| `accounts`、`account_groups`、`proxies` | 读号、分组、代理；只改 `accounts.proxy_id` |
| `usage_logs`、`request_connection_traces` | 读首字样本、对出代理，和首字趋势同一套查询 |

## 怎么关

- 页面上把开关关掉，点「保存设置」；或者 PUT `/config` 带 `enabled: false`。下一轮起不再判断、不再换。
- 关掉之后，已经换过、还在「统计中」的记录照样在 10 分钟内收尾定稿。
- 账号监控、换代理记录页面关着也能看。
- 只想停某个分组：把它从「指定分组」里去掉。
- 回退程序：迁移 311 只加列、放宽 CHECK，回退旧版不用回滚迁移。旧版读到 v2 的配置时，v2 没有的老字段按旧版默认值，判慢阈值会变成 2.5 秒（PR #12 版本的默认值），所以**回退前先关掉开关**，回退后重新设好再开。旧版要求统计窗口 5–60 分钟、最少请求数 1–1000，超出范围时旧版校验不过、整轮报错。

## 查问题

### 为什么这个号没换

先看账号监控里这个号的状态和数字：

| 看到的 | 原因 | 怎么办 |
|---|---|---|
| 页面顶部「已关闭」 | 开关没开 | 打开并保存 |
| 账号不可用 | 看备注：不是 OpenAI OAuth 号、影子号、不是 active、不可调度 | 这类号不参与 |
| 没有代理 | 号没挂代理 | 自动换代理只换已有代理的号 |
| 不到 N 个请求 | 请求数不够 `min_samples` | 见下面「请求数比预期少」 |
| 正常，但首字看着很慢 | p60 和均值只有一项超过阈值，或刚好等于阈值 | 两个数都要严格大于阈值 |
| 两项都超，但换代理记录里没有新记录 | 看这个号最近一条记录：同一代理、同一原因的 `no_candidate` 不会重复写，可能早就有一条了 | 按 `reason` 处理候选池 |
| 记录是 `no_candidate` | 候选池里没有合格的代理 | 看 `reason`；候选代理池表里看「已过期」和「现在几个号在用」 |
| 记录是 `failed` | 改账号代理失败 | 看 `reason` 里的错误 |
| 两项都超，一直没有任何记录 | 换之前号的代理被改了（只打日志）；后台任务没跑（实例是 gateway 角色、领导锁在别的实例上）；库里的配置不合法 | 查日志，见下表 |

请求数比预期少：只算流式、有首字、已经完成、开始时间在窗口里、记在指定分组下、走的是当前代理的请求。号刚换过代理时，窗口里旧代理上的请求不算，新代理要重新攒够请求数。

相关日志：

| 日志 | 含义 |
|---|---|
| `proxy_autoswitch_switched` | 换成了，带 `account_id` `from_proxy_id` `to_proxy_id` `event_id` |
| `proxy_autoswitch_account_changed_before_switch` | 换之前号的代理变了，放弃这次 |
| `proxy_autoswitch_attempt_failed` | 某个号这次换代理出错（读代理、读候选池、写记录失败） |
| `proxy_autoswitch_cycle_failed` | 整轮失败，比如配置读不出来、取号失败、取使用记录失败 |
| `proxy_autoswitch_measure_failed` `proxy_autoswitch_finish_measurement_failed` | 更新换后数据失败 |
| `proxy_autoswitch_cycle_panic` | 一轮里出了 panic，下一轮照常跑 |

### 为什么一个号连着换了好几次

v2 没有冷却。新代理上过了几分钟又攒够了请求数、两项又都超，就会再换。另外，对不上连接 trace 的使用记录按号**现在**的代理算，刚换完的几分钟里，旧代理上一部分对不上 trace 的请求可能被算到新代理头上。想少换，可以调大 `min_samples` 或 `window_minutes`，或者调高阈值。

### 账号监控和首字趋势的数对不上

两边取样规则一样，但账号监控只看**当前代理**上过去 `window_minutes` 分钟，算的是 p60 和均值；首字趋势按时间格看，不分代理（除非按代理拆），算的是平均、p50、p80。

## 版本变化

| PR | 提交 | 日期 | 改了什么 |
|---|---|---|---|
| #7 | `2a0c4aa23` | 11-04 | v1：上游响应头 p50 > 2.5 秒、10 分钟窗口、至少 10 个样本、连续 2 次才换；从所有空闲代理里挑（离过期 ≥ 7 天、优先同地区），比当前快 30% 才换、挑最快的；观察 15 分钟没变快就换回；同号 6 小时 1 次、全站每小时 5 个号；人工改过代理 6 小时不动；只改 `proxy_id`；迁移 307 |
| #7 | `885aa26fa` | 11-04 | 判慢指标可选 trace 首字，线上改成首字 10 秒；迁移 308 加 `metric` 列 |
| #7 | `dd667729c` | 11-04 | 同号冷却改成 10 分钟；换完不再换回，只记 `kept` / `not_faster`（迁移 309）；新建号 30 分钟内设的代理不算人工改过 |
| #11 | `67fdb1024` | 11-06 | 去掉「人工改过代理 6 小时不动」 |
| #11 | `3491ccd8b` | 11-06 | 新口径 `usage_first_token`（使用记录首字，和首字趋势共用取样）；观察期、冷却、上限、快多少、到期天数允许填 0 表示不限；迁移 310 |
| #12 | `20255b01a` `29cd00f75` | 11-06 | 删掉响应头、trace 首字两种口径，只留使用记录首字；页面去掉口径选择 |
| #13 | `2be7e924b` | 11-06 | v2：配置只剩 8 项；p60 和均值两个阈值同时超过马上换；去掉连续轮数、观察期、冷却、每小时上限、快多少、到期天数、换回；人工候选池 + 三种策略；`no_candidate` 去重；记录换前 N 分钟 / 换后 10 分钟的数据和新旧测速；新增 `/proxies`；删掉 `/events/:id/revert`；迁移 311 |
| #13 | `e5198fcd6` | 11-06 | 页面 v2：设置、候选代理池勾选和排序、账号监控、换代理记录前后对比；去掉换回按钮 |
