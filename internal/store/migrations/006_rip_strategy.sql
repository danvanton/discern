-- Rip strategies (DESIGN section 9.3, decisions D64-D71).
--
-- A job's strategy: '' (Direct) or 'backup' (Backup + remux).
ALTER TABLE jobs ADD COLUMN strategy TEXT NOT NULL DEFAULT '';

-- The strategy history: every Direct and Backup attempt, by disc (a hash of
-- its title shapes, decision D71) and MakeMKV version. A rip starts with
-- Backup + remux when the latest Direct attempt on the same disc and
-- version stalled (decision D66).
CREATE TABLE rip_attempts (
    id       INTEGER PRIMARY KEY,
    disc     TEXT    NOT NULL,
    makemkv  TEXT    NOT NULL,
    strategy TEXT    NOT NULL,             -- '' (Direct) or 'backup'
    result   TEXT    NOT NULL,             -- 'ok', 'stalled' or 'failed'
    job_id   INTEGER NOT NULL,
    at       INTEGER NOT NULL              -- Unix time, milliseconds, UTC
) STRICT;

CREATE INDEX rip_attempts_by_disc ON rip_attempts (disc, makemkv, strategy, id);
