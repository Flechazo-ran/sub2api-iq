-- 代理优选：判慢指标可选（首字 first_token / 响应头 response_headers）。
-- 每条记录存当时按哪个指标判的慢；观察期按这条记录自己的指标比较。
-- 已有记录都是按响应头判的，默认值就是 response_headers。
ALTER TABLE proxy_autoswitch_events
    ADD COLUMN IF NOT EXISTS metric VARCHAR(32) NOT NULL DEFAULT 'response_headers';

ALTER TABLE proxy_autoswitch_events
    DROP CONSTRAINT IF EXISTS proxy_autoswitch_events_metric_check;

ALTER TABLE proxy_autoswitch_events
    ADD CONSTRAINT proxy_autoswitch_events_metric_check
    CHECK (metric IN ('response_headers', 'first_token'));
