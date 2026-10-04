-- Store Chinese and English announcement text independently. Legacy title/content
-- remain the backward-compatible fallback for API consumers that have not adopted
-- localization fields yet.
ALTER TABLE announcements
    ADD COLUMN IF NOT EXISTS title_zh VARCHAR(200) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS title_en VARCHAR(200) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS content_zh TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS content_en TEXT NOT NULL DEFAULT '';
