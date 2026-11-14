-- Settings from the web UI's Settings page (decisions D21, D26): one row per
-- setting, the value as JSON. A setting with no row has its default.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;
