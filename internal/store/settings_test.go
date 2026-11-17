package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/danvanton/discern/internal/settings"
)

func TestSettingsStartAtTheirDefaults(t *testing.T) {
	s, _ := openTest(t)
	st, err := s.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st, settings.Defaults()) {
		t.Errorf("Settings = %+v; want the defaults %+v", st, settings.Defaults())
	}
}

func TestSettingsSaveAndReadBack(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	want := settings.Settings{Eject: false, AutoImport: settings.AutoOn, Languages: &settings.Languages{
		Audio: []string{"eng", "fre"}, Subtitles: []string{"eng"}, ForcedOnly: true,
	}}
	if err := s.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Settings = %+v, %v; want %+v", got, err, want)
	}

	// Clearing the languages brings back MakeMKV's own default.
	want.Languages = nil
	if err := s.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Settings(ctx); err != nil || got.Languages != nil {
		t.Fatalf("Settings = %+v, %v; want no languages", got, err)
	}
}

func TestSettingsFailClosedOnAValueThatDoesntParse(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('eject', 'yes please')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settings(ctx); err == nil {
		t.Fatal("a broken value was read as a default")
	}
}

func TestSeedEjectOnlyWhenNeverSaved(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	if seeded, err := s.SeedEject(ctx, false); err != nil || !seeded {
		t.Fatalf("SeedEject = %v, %v; want true, nil", seeded, err)
	}
	if st, _ := s.Settings(ctx); st.Eject {
		t.Error("eject is on after seeding it off")
	}
	// Once saved, the page's value wins over the old variable.
	s.SaveSettings(ctx, settings.Settings{Eject: true})
	if seeded, _ := s.SeedEject(ctx, false); seeded {
		t.Error("SeedEject replaced a saved setting")
	}
	if st, _ := s.Settings(ctx); !st.Eject {
		t.Error("the saved eject setting was overwritten")
	}
}

// Auto-import saves and reads back; unset is saved as off; a mode this
// version doesn't know is refused on saving, and read back as it is,
// leaving the other settings readable (the select step refuses it).
func TestAutoImportSetting(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	if st, _ := s.Settings(ctx); st.AutoImport != settings.AutoOff {
		t.Errorf("default auto-import %q; want off", st.AutoImport)
	}
	if err := s.SaveSettings(ctx, settings.Settings{Eject: true, AutoImport: settings.AutoTrial}); err != nil {
		t.Fatal(err)
	}
	if st, err := s.Settings(ctx); err != nil || st.AutoImport != settings.AutoTrial {
		t.Errorf("auto-import %q, %v; want trial", st.AutoImport, err)
	}
	if err := s.SaveSettings(ctx, settings.Settings{Eject: true}); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Settings(ctx); st.AutoImport != settings.AutoOff {
		t.Errorf("unset auto-import read back as %q; want off", st.AutoImport)
	}
	if err := s.SaveSettings(ctx, settings.Settings{AutoImport: "always"}); err == nil {
		t.Error("an unknown mode was saved")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE settings SET value = '"always"' WHERE key = 'auto_import'`); err != nil {
		t.Fatal(err)
	}
	if st, err := s.Settings(ctx); err != nil || st.AutoImport != "always" || !st.Eject {
		t.Errorf("an unknown saved mode: %+v, %v; want it as saved, and eject still read", st, err)
	}
}
