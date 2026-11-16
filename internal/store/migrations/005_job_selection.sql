-- What the selector suggested and why (decision D43): JSON, jobs.Selection
-- (titles, confidence, reasons, holds, TheDiscDB's names for the titles).
-- NULL before the Select step, and for jobs selected before this column.
ALTER TABLE jobs ADD COLUMN selection TEXT;
