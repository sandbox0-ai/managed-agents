-- +goose Up

CREATE SEQUENCE IF NOT EXISTS managed_agent_session_events_position_seq;

ALTER TABLE managed_agent_session_events
    ADD COLUMN IF NOT EXISTS position BIGINT;

ALTER TABLE managed_agent_session_events
    ALTER COLUMN position SET DEFAULT nextval('managed_agent_session_events_position_seq');

WITH ordered AS (
    SELECT id
    FROM managed_agent_session_events
    WHERE position IS NULL
    ORDER BY created_at ASC, id ASC
), numbered AS (
    SELECT id, nextval('managed_agent_session_events_position_seq') AS position
    FROM ordered
)
UPDATE managed_agent_session_events AS events
SET position = numbered.position
FROM numbered
WHERE events.id = numbered.id;

ALTER SEQUENCE managed_agent_session_events_position_seq
    OWNED BY managed_agent_session_events.position;

ALTER TABLE managed_agent_session_events
    ALTER COLUMN position SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_managed_agent_session_events_position
    ON managed_agent_session_events(position);

CREATE INDEX IF NOT EXISTS idx_managed_agent_session_events_session_position
    ON managed_agent_session_events(session_id, position);

-- +goose Down

DROP INDEX IF EXISTS idx_managed_agent_session_events_session_position;
DROP INDEX IF EXISTS idx_managed_agent_session_events_position;

ALTER TABLE managed_agent_session_events
    ALTER COLUMN position DROP DEFAULT;

ALTER TABLE managed_agent_session_events
    DROP COLUMN IF EXISTS position;

DROP SEQUENCE IF EXISTS managed_agent_session_events_position_seq;
