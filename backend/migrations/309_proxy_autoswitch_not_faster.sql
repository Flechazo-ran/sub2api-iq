-- 代理优选：换完观察不再换回原代理。没变快记 not_faster（不动账号代理，号还慢就过了冷却再换）。
-- reverted / revert_skipped 留给旧记录，自动流程不再产生。
-- 307 的 status CHECK 是列内联写的，PostgreSQL 自动命名为 proxy_autoswitch_events_status_check。
ALTER TABLE proxy_autoswitch_events
    DROP CONSTRAINT IF EXISTS proxy_autoswitch_events_status_check;

ALTER TABLE proxy_autoswitch_events
    ADD CONSTRAINT proxy_autoswitch_events_status_check
    CHECK (status IN (
        'observing', 'kept', 'kept_insufficient', 'not_faster', 'reverted', 'revert_skipped',
        'no_candidate', 'failed', 'manual_revert'));
