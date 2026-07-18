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

	// 1. Coleta todos os caminhos candidatos
	var candidates []string

	// Candidato 1: Diretório de configuração padrão do usuário
	if base, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(base, "subgen", filename))
	}

	// Candidato 2: Pasta .config no Home do usuário (caso UserConfigDir retorne algo diferente/inválido)
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "subgen", filename))
		candidates = append(candidates, filepath.Join(home, ".subgen", filename))
	}

	// Candidato 3: Diretório do próprio executável (como fallback absoluto)
	if exePath, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exePath), "subgen_config.json"))
	}

	// 2. Primeiro passo: se o arquivo de configuração já existe em algum dos candidatos, usa ele!
	// (Isso garante compatibilidade com configurações salvas anteriormente)
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	// 3. Segundo passo: se não existe, escolhe o primeiro cuja pasta de destino seja gravável
	for _, path := range candidates {
		dir := filepath.Dir(path)
		if isWritable(dir) {
			return path, nil
		}
	}

	// Se nenhum for gravável (caso extremo), retorna o primeiro candidato da lista como padrão
	if len(candidates) > 0 {
		return candidates[0], nil
	}

	return "", errors.New("nenhum diretório de configuração gravável foi encontrado")
}

func isWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return false
	}
	f.Close()
	_ = os.Remove(f.Name())
	return true
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
