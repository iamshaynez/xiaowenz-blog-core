-- +goose Up
-- Newsletter 全文邮件：记录草稿来源文章 URL，发送时用于「阅读原文」链接。
-- 手动撰写的草稿为空串（模板按空值隐藏链接按钮）。
ALTER TABLE newsletters ADD COLUMN post_url TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE newsletters DROP COLUMN post_url;
