-- A disc's own language choice, made on the review page (decision D34).
-- JSON: settings.Languages. NULL means the Settings page's choice applies.
ALTER TABLE jobs ADD COLUMN languages TEXT;
