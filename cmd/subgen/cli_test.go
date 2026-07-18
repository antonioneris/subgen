package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antonioneris/subgen/internal/config"
)

func TestHelp(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), nil, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "subgen translate") {
		t.Fatalf("help = %q", out.String())
	}
}

func TestConfigSetAndShowMasksToken(t *testing.T) {
	t.Setenv("SUBGEN_CONFIG_PATH", t.TempDir()+"/config.json")
	var out bytes.Buffer
	err := run(context.Background(), []string{"config", "set", "--language", "ptbr", "--token-stdin"}, strings.NewReader("sk-or-v1-secret1234\n"), &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run(context.Background(), []string{"config", "show"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pt-BR") {
		t.Fatalf("output = %q", out.String())
	}
	if strings.Contains(out.String(), "secret1234") || !strings.Contains(out.String(), "…1234") {
		t.Fatalf("token was not safely masked: %q", out.String())
	}
}

func TestConfigSetStoresOrderedSourceLanguages(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("SUBGEN_CONFIG_PATH", configPath)
	var out bytes.Buffer
	if err := run(context.Background(), []string{"config", "set", "--source", "eng, fre, ita"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.SourceLanguages, ",") != "en,fr,it" {
		t.Fatalf("sources = %#v", cfg.SourceLanguages)
	}
	out.Reset()
	if err := run(context.Background(), []string{"config", "show"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "en → fr → it") {
		t.Fatalf("show = %q", out.String())
	}
}

func TestConfigWizardGuidesAndSaves(t *testing.T) {
	t.Setenv("SUBGEN_CONFIG_PATH", t.TempDir()+"/config.json")
	// Direct DeepSeek, token, language, default model, no advanced settings, save.
	answers := "1\nsk-deepseek-wizard-5678\nptbr\n\nn\n\n"
	var out bytes.Buffer
	if err := run(context.Background(), []string{"config"}, strings.NewReader(answers), &out, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "wizard-5678") {
		t.Fatalf("wizard leaked token: %q", out.String())
	}
	if !strings.Contains(out.String(), "Configuração guiada") || !strings.Contains(out.String(), "Configuração salva") {
		t.Fatalf("unexpected wizard output: %q", out.String())
	}
	out.Reset()
	if err := run(context.Background(), []string{"config", "show"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "DeepSeek direto") || !strings.Contains(out.String(), "pt-BR") || !strings.Contains(out.String(), defaultModel) || !strings.Contains(out.String(), "…5678") {
		t.Fatalf("saved values not shown: %q", out.String())
	}
}

func TestConfigWizardSavesOrderedSourceFallbacks(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("SUBGEN_CONFIG_PATH", configPath)
	answers := "1\nsk-deepseek-fallback-5678\nptbr\n\ns\nEnglish, French, Spanish\n\n\n\n\n"
	var out bytes.Buffer
	if err := run(context.Background(), []string{"config"}, strings.NewReader(answers), &out, &out); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.SourceLanguages, ",") != "en,fr,es" {
		t.Fatalf("sources = %#v; output=%s", cfg.SourceLanguages, out.String())
	}
	if !strings.Contains(out.String(), "en → fr → es") || !strings.Contains(out.String(), "somente uma faixa") {
		t.Fatalf("wizard did not explain fallback behavior: %s", out.String())
	}
}

func TestSavedProviderPreservesLegacyOpenRouterConfig(t *testing.T) {
	cfg := config.Config{APIKey: "sk-or-existing", Model: defaultOpenRouterModel}
	if got := savedProvider(cfg); got != "openrouter" {
		t.Fatalf("provider = %q", got)
	}
}

func TestNormalizeLanguage(t *testing.T) {
	for input, want := range map[string]string{"ptbr": "pt-BR", "en_US": "en-US", "Português": "Português", "auto": "auto"} {
		if got := normalizeLanguage(input); got != want {
			t.Errorf("normalizeLanguage(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTranslateUsesConfiguredDefaultLanguage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SUBGEN_CONFIG_PATH", filepath.Join(dir, "config.json"))
	srtPath := filepath.Join(dir, "episode.srt")
	if err := os.WriteFile(srtPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nHello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"config", "set", "--language", "ptbr"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run(context.Background(), []string{"translate", srtPath, "--dry-run"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "episode.pt.srt") {
		t.Fatalf("configured language not used: %q", out.String())
	}
}

func TestAllowPathFirst(t *testing.T) {
	got := allowPathFirst([]string{"folder", "--to", "pt-BR", "--dry-run"})
	want := []string{"--to", "pt-BR", "--dry-run", "folder"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

func TestVersion(t *testing.T) {
	for _, cmd := range []string{"version", "--version", "-v"} {
		var out bytes.Buffer
		if err := run(context.Background(), []string{cmd}, strings.NewReader(""), &out, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out.String(), "subgen ") {
			t.Errorf("command %s output = %q, expected 'subgen <version>'", cmd, out.String())
		}
	}
}
