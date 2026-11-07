-- +goose Up
-- 统计改造：pageviews 变为有界热层（rollup 后按保留期清理），daily_stats 是
-- 永久日聚合层。site 维度为多站统计预留，当前恒为空串。
ALTER TABLE pageviews ADD COLUMN site TEXT NOT NULL DEFAULT '';

CREATE TABLE daily_stats (
    site TEXT NOT NULL DEFAULT '',
    day  TEXT NOT NULL,
    pv   INTEGER NOT NULL,
    uv   INTEGER NOT NULL,
    PRIMARY KEY (site, day)
);

-- +goose Down
DROP TABLE IF EXISTS daily_stats;
ALTER TABLE pageviews DROP COLUMN site;
