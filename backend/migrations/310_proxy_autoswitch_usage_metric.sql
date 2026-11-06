-- 代理优选：判慢口径加 usage_first_token（usage_logs.first_token_ms，和使用记录页、首字趋势一致）。
-- 只放宽 metric 的 CHECK，不改已有数据；旧版本程序不会写这个值，回滚程序不用回滚这条。
ALTER TABLE proxy_autoswitch_events
    DROP CONSTRAINT IF EXISTS proxy_autoswitch_events_metric_check;

ALTER TABLE proxy_autoswitch_events
    ADD CONSTRAINT proxy_autoswitch_events_metric_check
    CHECK (metric IN ('response_headers', 'first_token', 'usage_first_token'));
