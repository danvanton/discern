-- Jobs: one row per disc that went through Trayarr.
CREATE TABLE jobs (
    id         INTEGER PRIMARY KEY,
    drive      TEXT    NOT NULL,
    state      TEXT    NOT NULL,
    disc       TEXT,                       -- JSON: makemkv.Disc, the scan
    movie      TEXT,                       -- JSON: jobs.Movie
    titles     TEXT    NOT NULL DEFAULT '[]', -- JSON array of title numbers
    note       TEXT    NOT NULL DEFAULT '',
    failed_in  TEXT    NOT NULL DEFAULT '',
    progress   REAL    NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,           -- Unix time, milliseconds, UTC
    updated_at INTEGER NOT NULL
) STRICT;

-- One active job per drive, enforced by the database itself, so no bug in
-- Go code can start two rips on one drive. The list of states that are not
-- active must match jobs.State.Active; a test checks it.
CREATE UNIQUE INDEX jobs_one_active_per_drive
    ON jobs (drive)
    WHERE state NOT IN ('done', 'failed', 'cancelled');

-- Every transition, for the history page and for debugging.
CREATE TABLE job_events (
    id         INTEGER PRIMARY KEY,
    job_id     INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    at         INTEGER NOT NULL,
    from_state TEXT    NOT NULL,           -- '' for the job's creation
    to_state   TEXT    NOT NULL,
    note       TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX job_events_by_job ON job_events (job_id, id);
