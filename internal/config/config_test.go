package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Config{APIKey: "sk-or-secret", DefaultLanguage: "pt-BR", SourceLanguages: []string{"en", "fr"}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permissions = %o", perm)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, Config{}) {
		t.Fatalf("got %#v", cfg)
	}
}

func TestLoadMigratesBrokenAutoLanguage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"source_language":"au-TO"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceLanguage != "" || !reflect.DeepEqual(cfg.SourceLanguages, []string{"auto"}) {
		t.Fatalf("source = %q / %#v", cfg.SourceLanguage, cfg.SourceLanguages)
	}
}
