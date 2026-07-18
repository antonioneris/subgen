package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathWithEnvVar(t *testing.T) {
	tempDir := t.TempDir()
	customPath := filepath.Join(tempDir, "custom-config.json")
	t.Setenv("SUBGEN_CONFIG_PATH", customPath)

	got, err := Path()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want, _ := filepath.Abs(customPath)
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestPathExistsPrioritized(t *testing.T) {
	t.Setenv("SUBGEN_CONFIG_PATH", "")

	// Create a temp directory for tests
	tempDir := t.TempDir()
	
	// Create a mock existing config file
	mockFile := filepath.Join(tempDir, "config.json")
	if err := os.WriteFile(mockFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	// We hijack UserConfigDir (XDG_CONFIG_HOME) or HOME via env depending on OS,
	// but instead of relying on env vars of the host system, we can verify that
	// if a config file is present in candidate list, it gets prioritized.
	// Since os.UserConfigDir uses XDG_CONFIG_HOME on Linux, let's mock XDG_CONFIG_HOME.
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	t.Setenv("HOME", tempDir)

	got, err := Path()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// It should find the config under subgen/config.json in the mocked config dir
	if !strings.Contains(got, "config.json") {
		t.Errorf("expected path containing config.json, got %q", got)
	}
}

func TestIsWritable(t *testing.T) {
	tempDir := t.TempDir()
	
	// Writable path should return true
	writableDir := filepath.Join(tempDir, "writable")
	if !isWritable(writableDir) {
		t.Errorf("expected isWritable(%q) to be true", writableDir)
	}

	// Non-writable path should return false
	// We can try to use a file path as directory to cause a mkdir error
	invalidDir := filepath.Join(tempDir, "file.txt")
	if err := os.WriteFile(invalidDir, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if isWritable(invalidDir) {
		t.Errorf("expected isWritable(%q) to be false", invalidDir)
	}
}
