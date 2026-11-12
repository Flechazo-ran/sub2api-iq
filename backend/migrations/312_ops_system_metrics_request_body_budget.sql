-- ops_system_metrics 增加请求体内存总额度的用量（#16），按采集窗口记录：
-- 额度上限、窗口内的峰值占用（MB），以及窗口内因额度用满返回 503 的次数。
-- 都可为空：额度关闭（配置为 0）时不写。
ALTER TABLE ops_system_metrics
    ADD COLUMN IF NOT EXISTS request_body_budget_limit_mb BIGINT,
    ADD COLUMN IF NOT EXISTS request_body_budget_peak_mb BIGINT,
    ADD COLUMN IF NOT EXISTS request_body_budget_rejected BIGINT;
