-- 代理评分预计算：每个代理每天一行，存当天流式请求首字的直方图、条数和总和。
-- 后台每 10 分钟重算「今天」，「昨天及以前」算一次就不再动；评分页只读这张小表，
-- 3/7/30 天的 p50/p80/均值由直方图合并得出（误差不超过一个桶宽 250ms）。
-- 不加外键：代理删了历史照样留着，评分页只展示未删除的代理。
CREATE TABLE IF NOT EXISTS proxy_score_daily (
    day         DATE        NOT NULL,
    proxy_id    BIGINT      NOT NULL,
    samples     BIGINT      NOT NULL DEFAULT 0,
    sum_ms      BIGINT      NOT NULL DEFAULT 0,
    -- histogram[i] 是首字落在 [i*250, (i+1)*250) 毫秒的请求数；最后一个桶收 60 秒及以上。
    histogram   BIGINT[]    NOT NULL DEFAULT '{}',
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (day, proxy_id)
);

CREATE INDEX IF NOT EXISTS proxy_score_daily_proxy_day_idx ON proxy_score_daily (proxy_id, day);
