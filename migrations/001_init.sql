CREATE TABLE log_events (
    event_id         uuid        NOT NULL,
    event_time       timestamptz NOT NULL,
    received_at      timestamptz NOT NULL DEFAULT now(),
    level            text        NOT NULL
        CHECK (level IN ('INFO', 'WARN', 'ERROR', 'DEBUG')),

    project_key      text        NOT NULL,
    node_key         text        NOT NULL,
    locked           boolean     NOT NULL DEFAULT false,

    request_ip       inet        NOT NULL,
    member           text        NOT NULL DEFAULT '',
    session_id       text        NOT NULL DEFAULT '',
    request_method   text        NOT NULL,
    request_url      text        NOT NULL,
    request_headers  jsonb,
    request_params   jsonb,

    error_scene      text        NOT NULL,
    error_message    text        NOT NULL,
    error_file       text,
    error_line       integer,
    error_stack      text,

    PRIMARY KEY (level, event_time, event_id)
) PARTITION BY LIST (level);

CREATE TABLE log_events_error
    PARTITION OF log_events
    FOR VALUES IN ('ERROR')
    PARTITION BY RANGE (event_time);

CREATE TABLE log_events_other
    PARTITION OF log_events
    FOR VALUES IN ('INFO', 'WARN', 'DEBUG')
    PARTITION BY RANGE (event_time);

CREATE INDEX idx_log_events_time
    ON log_events USING brin (event_time);

CREATE INDEX idx_log_events_time_page
    ON log_events (event_time DESC, event_id DESC);

CREATE INDEX idx_log_events_project_time
    ON log_events (project_key, event_time DESC, event_id DESC);

CREATE INDEX idx_log_events_node_time
    ON log_events (node_key, event_time DESC, event_id DESC);

CREATE INDEX idx_log_events_scene_time
    ON log_events (error_scene, event_time DESC, event_id DESC);

CREATE INDEX idx_log_events_ip_time
    ON log_events (request_ip, event_time DESC, event_id DESC);

CREATE INDEX idx_log_events_member_time
    ON log_events (member, event_time DESC, event_id DESC);

CREATE INDEX idx_log_events_session_id_time
    ON log_events (session_id, event_time DESC, event_id DESC);

CREATE TABLE project_tokens (
    id          uuid        PRIMARY KEY,
    project_key text        NOT NULL,
    name        text        NOT NULL,
    token_hash  text        NOT NULL UNIQUE,
    enabled     boolean     NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE system_settings (
    setting_key   text        PRIMARY KEY,
    setting_value jsonb       NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now()
);

INSERT INTO system_settings (setting_key, setting_value)
VALUES
    ('retention_days', '30'::jsonb)
ON CONFLICT (setting_key) DO NOTHING;

CREATE TABLE log_dimensions (
    project_key  text        NOT NULL,
    node_key     text        NOT NULL,
    level        text        NOT NULL
        CHECK (level IN ('INFO', 'WARN', 'ERROR', 'DEBUG')),
    last_seen_at timestamptz NOT NULL,

    PRIMARY KEY (project_key, node_key, level)
);
