-- migrate:up

CREATE TABLE log_chunks (
    id         BIGSERIAL PRIMARY KEY,
    org_id     BIGINT NOT NULL REFERENCES orgs(id)  ON DELETE CASCADE,
    task_id    BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    version    BIGINT NOT NULL,  -- the run (task.Version) the bytes belong to; 0 = pre-run preamble
    data       BYTEA  NOT NULL,  -- raw log bytes, opaque to the server
    created_at TIMESTAMP NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC')
);

-- Keyset reads per task, matching idx_events_task_id_id.
CREATE INDEX idx_log_chunks_task_id_id ON log_chunks (task_id, id);

-- migrate:down

DROP TABLE IF EXISTS log_chunks;
