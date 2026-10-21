-- Existing company RDS databases recorded the textsearch migration as 111.
-- Reconcile the official 111 table without changing migration history.
CREATE TABLE IF NOT EXISTS model_catalog_configs (
    id INTEGER PRIMARY KEY,
    version BIGINT NOT NULL DEFAULT 0,
    overlay JSONB NOT NULL,
    history JSONB NOT NULL,
    updated_by VARCHAR(36) NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (id = 1)
);
INSERT INTO model_catalog_configs (id, version, overlay, history)
VALUES (1, 0, '{"providers":{}}', '[]') ON CONFLICT DO NOTHING;

DO $$
BEGIN
    IF current_setting('app.skip_embedding', true) = 'true' THEN
        RETURN;
    END IF;

    -- Nullable derived text leaves existing ParadeDB writes unchanged.
    ALTER TABLE embeddings ADD COLUMN IF NOT EXISTS search_content TEXT;
    IF current_setting('app.postgres_search_extension', true) IS DISTINCT FROM 'pg_textsearch' THEN
        RETURN;
    END IF;

    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_search') THEN
        RAISE EXCEPTION 'pg_textsearch requires a target database without pg_search; keep the source database unchanged';
    END IF;
    CREATE EXTENSION IF NOT EXISTS pg_textsearch;
    CREATE INDEX IF NOT EXISTS embeddings_textsearch_idx ON embeddings
        USING bm25 (search_content) WITH (text_config = 'simple');
END $$;
