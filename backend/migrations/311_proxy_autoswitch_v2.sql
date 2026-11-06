-- 代理优选 v2：换代理记录改成「换前数据 + 策略 + 两个代理测速 + 换后 10 分钟数据」。
-- 老行原样保留，新列在老行上是 NULL；status 的 CHECK 同时允许新旧状态。
ALTER TABLE proxy_autoswitch_events
    ADD COLUMN IF NOT EXISTS group_id BIGINT,
    ADD COLUMN IF NOT EXISTS strategy VARCHAR(16),
    ADD COLUMN IF NOT EXISTS window_minutes INTEGER,
    ADD COLUMN IF NOT EXISTS before_p60_ms BIGINT,
    ADD COLUMN IF NOT EXISTS before_mean_ms BIGINT,
    ADD COLUMN IF NOT EXISTS before_duration_mean_ms BIGINT,
    ADD COLUMN IF NOT EXISTS after_p60_ms BIGINT,
    ADD COLUMN IF NOT EXISTS after_mean_ms BIGINT,
    ADD COLUMN IF NOT EXISTS after_duration_mean_ms BIGINT,
    ADD COLUMN IF NOT EXISTS from_probe_ms BIGINT,
    ADD COLUMN IF NOT EXISTS to_probe_ms BIGINT,
    ADD COLUMN IF NOT EXISTS after_finished_at TIMESTAMPTZ;

ALTER TABLE proxy_autoswitch_events
    DROP CONSTRAINT IF EXISTS proxy_autoswitch_events_status_check;

ALTER TABLE proxy_autoswitch_events
    ADD CONSTRAINT proxy_autoswitch_events_status_check
    CHECK (status IN (
        'measuring', 'done', 'no_candidate', 'failed',
        'observing', 'kept', 'kept_insufficient', 'not_faster', 'reverted', 'revert_skipped', 'manual_revert'));

CREATE INDEX IF NOT EXISTS proxy_autoswitch_events_measuring_idx
    ON proxy_autoswitch_events (id) WHERE status = 'measuring';
