package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/antonioneris/subgen/internal/language"
)

const filename = "config.json"

type Config struct {
	Provider        string   `json:"provider,omitempty"`
	APIKey          string   `json:"openrouter_api_key,omitempty"`
	DeepSeekAPIKey  string   `json:"deepseek_api_key,omitempty"`
	DefaultLanguage string   `json:"default_language,omitempty"`
	SourceLanguage  string   `json:"source_language,omitempty"`
	SourceLanguages []string `json:"source_languages,omitempty"`
	Model           string   `json:"model,omitempty"`
	BatchSize       int      `json:"batch_size,omitempty"`
	Parallelism     int      `json:"parallelism,omitempty"`
	Retries         int      `json:"retries,omitempty"`
	TimeoutSeconds  int      `json:"timeout_seconds,omitempty"`
}

// Path returns the platform-native config path. SUBGEN_CONFIG_PATH exists for
// portable installs and automated tests.
func Path() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("SUBGEN_CONFIG_PATH")); custom != "" {
		return filepath.Abs(custom)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("localizar diretório de configuração: %w", err)
	}
	return filepath.Join(base, "subgen", filename), nil
}

func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("abrir configuração: %w", err)
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 64*1024))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("ler configuração %s: %w", path, err)
	}
	// 0.3 normalized the special value "auto" as the language tag "au-TO".
	// Migrate it in memory so existing installations immediately behave right.
	if strings.EqualFold(cfg.SourceLanguage, "au-TO") {
		cfg.SourceLanguage = "auto"
	}
	if len(cfg.SourceLanguages) == 0 && strings.TrimSpace(cfg.SourceLanguage) != "" {
		cfg.SourceLanguages = language.ParseOrdered(cfg.SourceLanguage)
	}
	if len(cfg.SourceLanguages) > 0 {
		normalized := language.ParseOrdered(strings.Join(cfg.SourceLanguages, ","))
		cfg.SourceLanguages = normalized
		cfg.SourceLanguage = ""
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	dir := filepath.Dir(path)
	_, statErr := os.Stat(dir)
	createdDir := errors.Is(statErr, os.ErrNotExist)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("criar diretório de configuração: %w", err)
	}
	// Do not chmod an arbitrary existing parent selected with
	// SUBGEN_CONFIG_PATH. The standard dedicated subgen directory is safe to
	// tighten even when it already existed.
	if createdDir || filepath.Base(dir) == "subgen" {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("proteger diretório de configuração: %w", err)
		}
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("criar configuração temporária: %w", err)
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("proteger configuração: %w", err)
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(cfg); err != nil {
		return fmt.Errorf("codificar configuração: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sincronizar configuração: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fechar configuração: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("salvar configuração: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("proteger configuração salva: %w", err)
	}
	ok = true
	return nil
}
