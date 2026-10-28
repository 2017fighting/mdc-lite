-- +goose Up
-- The 7 business tables of V1 (spec §11). No Down section: migrations only
-- ever move forward.

CREATE TABLE batches (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger      TEXT    NOT NULL CHECK (trigger IN ('manual', 'monitor', 'webhook')),
    trigger_ref  TEXT    NOT NULL DEFAULT '',
    pipeline_id  TEXT    NOT NULL,
    status       TEXT    NOT NULL CHECK (status IN ('running', 'succeeded', 'partial', 'failed', 'cancelled')),
    total        INTEGER NOT NULL DEFAULT 0,
    tasks        INTEGER NOT NULL DEFAULT 0,
    succeeded    INTEGER NOT NULL DEFAULT 0,
    partial      INTEGER NOT NULL DEFAULT 0,
    failed       INTEGER NOT NULL DEFAULT 0,
    needs_number INTEGER NOT NULL DEFAULT 0,
    skipped      INTEGER NOT NULL DEFAULT 0,
    error        TEXT    NOT NULL DEFAULT '',
    started_at   TEXT    NOT NULL,
    finished_at  TEXT
);

CREATE TABLE tasks (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    status        TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'partial', 'failed', 'needs_number', 'cancelled')),
    number        TEXT    NOT NULL DEFAULT '',
    batch_id      INTEGER NOT NULL REFERENCES batches (id) ON DELETE CASCADE,
    pipeline_id   TEXT    NOT NULL,
    metadata_json TEXT    NOT NULL DEFAULT '{}',
    last_error    TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);

CREATE TABLE task_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     INTEGER NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    run_no      INTEGER NOT NULL,
    status      TEXT    NOT NULL CHECK (status IN ('running', 'succeeded', 'partial', 'failed', 'needs_number', 'interrupted', 'cancelled')),
    started_at  TEXT    NOT NULL,
    finished_at TEXT,
    error       TEXT    NOT NULL DEFAULT ''
);

-- Exactly one of batch_id / task_run_id is set: a step run belongs either to the
-- batch-level prefix or to one task run.
CREATE TABLE step_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    idx          INTEGER NOT NULL,
    type         TEXT    NOT NULL,
    name         TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL,
    started_at   TEXT    NOT NULL,
    ms           INTEGER NOT NULL DEFAULT 0,
    summary_json TEXT    NOT NULL DEFAULT '{}',
    error        TEXT    NOT NULL DEFAULT '',
    batch_id     INTEGER REFERENCES batches (id) ON DELETE CASCADE,
    task_run_id  INTEGER REFERENCES task_runs (id) ON DELETE CASCADE,
    CHECK ((batch_id IS NULL) <> (task_run_id IS NULL))
);

CREATE TABLE source_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    step_run_id  INTEGER NOT NULL REFERENCES step_runs (id) ON DELETE CASCADE,
    source_id    TEXT    NOT NULL,
    status       TEXT    NOT NULL CHECK (status IN ('ok', 'notfound', 'failed')),
    err          TEXT    NOT NULL DEFAULT '',
    reason       TEXT    CHECK (reason IS NULL OR reason IN ('egress_unavailable', 'timeout', 'blocked', 'cooldown', 'http_5xx', 'parse')),
    fields_json  TEXT    NOT NULL DEFAULT '{}',
    ms           INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE task_files (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id         INTEGER NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    path            TEXT    NOT NULL,
    kind            TEXT    NOT NULL CHECK (kind IN ('video', 'companion')),
    role            TEXT    NOT NULL CHECK (role IN ('primary', 'part', 'companion')),
    part_index      INTEGER NOT NULL DEFAULT 0,
    follows_part    INTEGER NOT NULL DEFAULT 0,
    size            INTEGER NOT NULL DEFAULT 0,
    mtime           TEXT    NOT NULL DEFAULT '',
    active          INTEGER NOT NULL DEFAULT 1,
    assigned_number TEXT    NOT NULL DEFAULT '',
    is_part         INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE file_rows (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     INTEGER NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    task_run_id INTEGER NOT NULL REFERENCES task_runs (id) ON DELETE CASCADE,
    step_run_id INTEGER NOT NULL REFERENCES step_runs (id) ON DELETE CASCADE,
    kind        TEXT    NOT NULL CHECK (kind IN ('video', 'image', 'nfo', 'companion')),
    role        TEXT    NOT NULL CHECK (role IN ('video', 'poster', 'fanart', 'extrafanart', 'nfo', 'companion')),
    idx         INTEGER NOT NULL DEFAULT 0,
    src         TEXT    NOT NULL DEFAULT '',
    dst         TEXT    NOT NULL,
    bytes       INTEGER NOT NULL DEFAULT 0,
    sha256      TEXT    NOT NULL DEFAULT '',
    action      TEXT    NOT NULL CHECK (action IN ('created', 'overwritten', 'skipped', 'deleted')),
    created_at  TEXT    NOT NULL
);

-- Dedup gate one: a path may be held by at most one non-terminal task.
CREATE UNIQUE INDEX ux_task_files_active_path ON task_files (path) WHERE active = 1;
CREATE UNIQUE INDEX ux_task_files_task_path ON task_files (task_id, path);
CREATE INDEX ix_tasks_status ON tasks (status);
CREATE INDEX ix_tasks_batch ON tasks (batch_id);
CREATE INDEX ix_tasks_created ON tasks (created_at);
CREATE INDEX ix_task_runs_task ON task_runs (task_id, run_no);
CREATE INDEX ix_step_runs_batch ON step_runs (batch_id);
CREATE INDEX ix_step_runs_task_run ON step_runs (task_run_id);
CREATE INDEX ix_source_runs_step_run ON source_runs (step_run_id);
CREATE INDEX ix_source_runs_source ON source_runs (source_id);
CREATE INDEX ix_file_rows_task_run ON file_rows (task_run_id);
