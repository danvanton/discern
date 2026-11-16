-- Why a failed job failed, for the failure page (decisions D35-D37).
-- JSON: jobs.Failure (kind, MakeMKV's last messages, diagnostics folder).
-- NULL for a job that hasn't failed, or failed before this column existed.
ALTER TABLE jobs ADD COLUMN failure TEXT;
