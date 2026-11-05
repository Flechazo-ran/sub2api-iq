-- 代理优选（首字慢自动换代理）：每次判慢后的处理记一行（换了、没候选、失败），
-- 换完的观察结果也写在同一行。号的冷却和全站每小时上限按这张表算，重启不丢。
-- 不加外键：账号/代理删了记录照样留着（名字存了快照）。
CREATE TABLE IF NOT EXISTS proxy_autoswitch_events (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    account_id BIGINT NOT NULL,
    status VARCHAR(32) NOT NULL CHECK (status IN (
        'observing', 'kept', 'kept_insufficient', 'reverted', 'revert_skipped',
        'no_candidate', 'failed', 'manual_revert')),
    reason TEXT NOT NULL DEFAULT '',
    from_proxy_id BIGINT,
    from_proxy_label VARCHAR(255) NOT NULL DEFAULT '',
    from_region VARCHAR(16) NOT NULL DEFAULT '',
    to_proxy_id BIGINT,
    to_proxy_label VARCHAR(255),
    to_region VARCHAR(16),
    before_p50_ms BIGINT,
    before_samples INTEGER NOT NULL DEFAULT 0,
    current_probe_ms BIGINT,
    candidate_probe_ms BIGINT,
    after_p50_ms BIGINT,
    after_samples INTEGER NOT NULL DEFAULT 0,
    candidates JSONB NOT NULL DEFAULT '[]'::jsonb,
    switched_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS proxy_autoswitch_events_account_idx
    ON proxy_autoswitch_events (account_id, id DESC);
CREATE INDEX IF NOT EXISTS proxy_autoswitch_events_created_idx
    ON proxy_autoswitch_events (created_at DESC);
CREATE INDEX IF NOT EXISTS proxy_autoswitch_events_observing_idx
    ON proxy_autoswitch_events (id) WHERE status = 'observing';

-- 每个号上次检查时看到的代理。下次看到的代理不一样、又不是本功能换的，
-- 就记 external_change_at（人工改过代理），6 小时内不自动改。
CREATE TABLE IF NOT EXISTS proxy_autoswitch_account_states (
    account_id BIGINT PRIMARY KEY,
    proxy_id BIGINT,
    seen_at TIMESTAMPTZ NOT NULL,
    external_change_at TIMESTAMPTZ
);
