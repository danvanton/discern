package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/danvanton/discern/internal/settings"
)

// Settings reads the Settings page's values. A setting that was never saved
// has its default; an unknown key (from a newer Discern) is ignored.
func (s *Store) Settings(ctx context.Context) (settings.Settings, error) {
	st := settings.Defaults()
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return st, err
		}
		var target any
		switch key {
		case "eject":
			target = &st.Eject
		case "languages":
			target = &st.Languages
		default:
			continue
		}
		// Fail closed: a value that doesn't parse is an error, not a default.
		if err := json.Unmarshal([]byte(value), target); err != nil {
			return settings.Defaults(), fmt.Errorf("setting %s: %w", key, err)
		}
	}
	return st, rows.Err()
}

// SaveSettings writes every setting in one transaction. Nil languages remove
// the saved choice, so MakeMKV's own default applies again.
func (s *Store) SaveSettings(ctx context.Context, st settings.Settings) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	values := map[string]any{"eject": st.Eject}
	if st.Languages != nil {
		values["languages"] = st.Languages
	} else if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = 'languages'`); err != nil {
		return err
	}
	for key, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
			key, string(b)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SeedEject saves eject only if it was never saved, and reports whether it
// did. It carries a DISCERN_EJECT value over to the Settings page once.
func (s *Store) SeedEject(ctx context.Context, eject bool) (bool, error) {
	b, _ := json.Marshal(eject)
	res, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('eject', ?) ON CONFLICT (key) DO NOTHING`, string(b))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
