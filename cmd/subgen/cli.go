package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/antonioneris/subgen/internal/app"
	"github.com/antonioneris/subgen/internal/config"
	langutil "github.com/antonioneris/subgen/internal/language"
	"github.com/antonioneris/subgen/internal/media"
	terminalui "github.com/antonioneris/subgen/internal/ui"
)

var version = "0.17.1"

const defaultProvider = "deepseek"
const defaultModel = "deepseek-v4-flash"
const defaultOpenRouterModel = "deepseek/deepseek-v4-flash"
const defaultLanguage = "pt-BR"
const defaultRetries = 3
const defaultParallelism = 4
const defaultTimeout = 15 * time.Minute

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printHelp(stdout)
		return nil
	}
	service := app.Service{Out: stdout, Err: stderr}
	switch args[0] {
	case "help", "-h", "--help":
		printHelp(stdout)
		return nil
	case "version", "--version":
		fmt.Fprintf(stdout, "subgen %s\n", version)
		return nil
	case "translate":
		return runTranslate(ctx, service, args[1:], stdin, stderr)
	case "info":
		return runInfo(ctx, service, args[1:], stderr)
	case "inspect":
		return runInspect(ctx, service, args[1:], stderr)
	case "config":
		return runConfig(args[1:], stdin, stdout, stderr)
	default:
		return fmt.Errorf("comando desconhecido %q; use 'subgen help'", args[0])
	}
}

func runInfo(ctx context.Context, service app.Service, args []string, stderr io.Writer) error {
	configPath, err := config.Path()
	if err != nil {
		return err
	}
	saved, err := config.Load(configPath)
	if err != nil {
		return err
	}
	args = allowPathFirst(args)
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := app.Options{Track: -1}
	var sourceList string
	providerDefault := envOr("SUBGEN_PROVIDER", savedProvider(saved))
	targetDefault := envOr("SUBGEN_LANGUAGE", saved.DefaultLanguage)
	fs.StringVar(&opts.Target, "to", targetDefault, "idioma de destino configurado")
	fs.StringVar(&sourceList, "from", strings.Join(savedSourceLanguages(saved), ","), "idiomas de origem por prioridade")
	fs.StringVar(&opts.Provider, "provider", providerDefault, "provedor: deepseek ou openrouter")
	fs.StringVar(&opts.Model, "model", envOr("SUBGEN_MODEL", savedModel(saved, providerDefault)), "modelo usado na estimativa")
	fs.IntVar(&opts.BatchSize, "batch", saved.BatchSize, "tamanho manual das partes; 0 divide automaticamente")
	fs.IntVar(&opts.Parallelism, "parallel", positiveOr(saved.Parallelism, defaultParallelism), "chamadas simultâneas (1 a 8)")
	fs.BoolVar(&opts.Recursive, "recursive", true, "buscar em subpastas")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("informe exatamente um arquivo ou pasta")
	}
	opts.Target = normalizeLanguage(opts.Target)
	if opts.Target == "" {
		return fmt.Errorf("configure o idioma de destino executando 'subgen config'")
	}
	opts.SourceLanguages = langutil.ParseOrdered(sourceList)
	if len(opts.SourceLanguages) == 0 {
		return fmt.Errorf("--from precisa ter ao menos um idioma ou auto")
	}
	opts.Source = firstTranslationSource(opts.SourceLanguages)
	opts.Provider = normalizeProvider(opts.Provider)
	if opts.Provider == "" {
		return fmt.Errorf("--provider deve ser deepseek ou openrouter")
	}
	if opts.Provider == "deepseek" {
		opts.APIKey = envOr("DEEPSEEK_API_KEY", saved.DeepSeekAPIKey)
	} else {
		opts.APIKey = envOr("OPENROUTER_API_KEY", saved.APIKey)
	}
	// An inventory cannot stop for an interactive choice. When the configured
	// source is auto, estimate with the best complete textual track.
	opts.SelectTrack = func(_ context.Context, _ string, tracks []media.SubtitleTrack) (int, error) {
		best := tracks[0]
		for _, track := range tracks[1:] {
			if track.Default && !best.Default {
				best = track
			}
		}
		return best.Index, nil
	}
	return service.Info(ctx, fs.Arg(0), opts)
}

func runTranslate(ctx context.Context, service app.Service, args []string, stdin io.Reader, stderr io.Writer) error {
	configPath, err := config.Path()
	if err != nil {
		return err
	}
	saved, err := config.Load(configPath)
	if err != nil {
		return err
	}
	args = allowPathFirst(args)
	fs := flag.NewFlagSet("translate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts app.Options
	var sourceList string
	providerDefault := envOr("SUBGEN_PROVIDER", savedProvider(saved))
	targetDefault := envOr("SUBGEN_LANGUAGE", saved.DefaultLanguage)
	fs.StringVar(&opts.Target, "to", targetDefault, "idioma de destino (ex.: pt-BR)")
	fs.StringVar(&opts.Target, "t", targetDefault, "atalho para --to")
	fs.StringVar(&sourceList, "from", strings.Join(savedSourceLanguages(saved), ","), "idiomas de origem por prioridade (ex.: en,fr,es)")
	fs.StringVar(&opts.Provider, "provider", providerDefault, "provedor de IA: deepseek ou openrouter")
	fs.StringVar(&opts.Model, "model", envOr("SUBGEN_MODEL", savedModel(saved, providerDefault)), "modelo no provedor escolhido")
	fs.IntVar(&opts.BatchSize, "batch", 0, "tamanho manual das partes; 0 divide automaticamente por tokens")
	fs.IntVar(&opts.Parallelism, "parallel", positiveOr(saved.Parallelism, defaultParallelism), "chamadas simultâneas (1 a 8)")
	fs.IntVar(&opts.Retries, "retries", positiveOr(saved.Retries, defaultRetries), "tentativas adicionais em falhas temporárias")
	fs.DurationVar(&opts.Timeout, "timeout", savedTimeout(saved), "tempo máximo sem receber dados da API")
	fs.IntVar(&opts.Track, "track", -1, "índice da faixa embutida; pergunta se houver várias")
	fs.BoolVar(&opts.Recursive, "recursive", true, "buscar em subpastas")
	fs.BoolVar(&opts.Overwrite, "overwrite", false, "substituir saídas existentes")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "mostrar o trabalho sem chamar a IA")
	fs.BoolVar(&opts.NormalizeEffects, "clean-effects", false, "limpar animações e desenhos ASS antes de traduzir")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("informe exatamente um arquivo ou pasta")
	}
	opts.Target = normalizeLanguage(opts.Target)
	opts.SourceLanguages = langutil.ParseOrdered(sourceList)
	if len(opts.SourceLanguages) == 0 {
		return fmt.Errorf("--from precisa ter ao menos um idioma ou auto")
	}
	opts.Source = firstTranslationSource(opts.SourceLanguages)
	opts.Provider = normalizeProvider(opts.Provider)
	if opts.Provider == "" {
		return fmt.Errorf("--provider deve ser deepseek ou openrouter")
	}
	if opts.Provider == "deepseek" {
		opts.APIKey = envOr("DEEPSEEK_API_KEY", saved.DeepSeekAPIKey)
		opts.Endpoint = os.Getenv("SUBGEN_DEEPSEEK_ENDPOINT")
	} else {
		opts.APIKey = envOr("OPENROUTER_API_KEY", saved.APIKey)
		opts.Endpoint = os.Getenv("SUBGEN_OPENROUTER_ENDPOINT")
	}
	opts.SelectTrack = func(ctx context.Context, path string, tracks []media.SubtitleTrack) (int, error) {
		return terminalui.SelectTrack(ctx, stdin, service.Out, path, tracks)
	}
	if !opts.DryRun && opts.APIKey == "" {
		return fmt.Errorf("configure a chave executando 'subgen config'")
	}
	return service.Translate(ctx, fs.Arg(0), opts)
}

func runConfig(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return runConfigWizard(path, stdin, stdout)
	}
	if args[0] == "show" {
		if len(args) > 1 {
			return fmt.Errorf("'config show' não recebe argumentos")
		}
		cfg, err := config.Load(path)
		if err != nil {
			return err
		}
		provider := savedProvider(cfg)
		fmt.Fprintf(stdout, "Arquivo: %s\nProvedor: %s\nIdioma de saída: %s\nIdiomas de origem: %s\nModelo: %s\nProcessamento paralelo: %d\nLimite sem dados: %s\nRetries: %d\nChave DeepSeek: %s\nChave OpenRouter: %s\n",
			path,
			providerLabel(provider),
			valueOr(cfg.DefaultLanguage, "não configurado"),
			langutil.FormatOrdered(savedSourceLanguages(cfg)),
			savedModel(cfg, provider),
			positiveOr(cfg.Parallelism, defaultParallelism),
			savedTimeout(cfg),
			positiveOr(cfg.Retries, defaultRetries),
			maskedToken(cfg.DeepSeekAPIKey),
			maskedToken(cfg.APIKey),
		)
		return nil
	}
	if args[0] == "path" {
		if len(args) != 1 {
			return fmt.Errorf("'config path' não recebe argumentos")
		}
		fmt.Fprintln(stdout, path)
		return nil
	}
	if args[0] == "set" {
		args = args[1:]
	} else if !strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("subcomando config desconhecido %q; use set, show ou path", args[0])
	}

	fs := flag.NewFlagSet("config set", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var token, deepSeekToken, provider, language, source, model string
	var batchSize, retries, parallelism int
	var timeout time.Duration
	var tokenStdin, clearToken bool
	fs.StringVar(&token, "token", "", "chave do OpenRouter")
	fs.StringVar(&deepSeekToken, "deepseek-token", "", "chave da API direta da DeepSeek")
	fs.StringVar(&provider, "provider", "", "provedor: deepseek ou openrouter")
	fs.StringVar(&language, "language", "", "idioma padrão de saída")
	fs.StringVar(&source, "source", "", "idioma padrão de origem")
	fs.StringVar(&model, "model", "", "modelo padrão no OpenRouter")
	fs.IntVar(&batchSize, "batch", 0, "número de blocos por chamada")
	fs.IntVar(&parallelism, "parallel", 0, "chamadas simultâneas, de 1 a 8")
	fs.IntVar(&retries, "retries", 0, "tentativas adicionais")
	fs.DurationVar(&timeout, "timeout", 0, "tempo máximo sem receber dados da API")
	fs.BoolVar(&tokenStdin, "token-stdin", false, "ler a chave da entrada padrão")
	fs.BoolVar(&clearToken, "clear-token", false, "remover a chave salva")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("argumento inesperado %q", fs.Arg(0))
	}
	if token != "" && tokenStdin {
		return fmt.Errorf("use apenas um entre --token e --token-stdin")
	}
	if clearToken && (token != "" || tokenStdin) {
		return fmt.Errorf("--clear-token não pode ser combinado com uma nova chave")
	}
	if tokenStdin {
		data, err := io.ReadAll(io.LimitReader(stdin, 16*1024))
		if err != nil {
			return fmt.Errorf("ler chave da entrada padrão: %w", err)
		}
		token = strings.TrimSpace(string(data))
		if token == "" {
			return fmt.Errorf("nenhuma chave recebida pela entrada padrão")
		}
	}
	if token == "" && deepSeekToken == "" && provider == "" && language == "" && source == "" && model == "" && batchSize == 0 && parallelism == 0 && retries == 0 && timeout == 0 && !clearToken {
		return fmt.Errorf("nenhuma configuração informada")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if token != "" {
		cfg.APIKey = strings.TrimSpace(token)
	}
	if deepSeekToken != "" {
		cfg.DeepSeekAPIKey = strings.TrimSpace(deepSeekToken)
	}
	if provider != "" {
		cfg.Provider = normalizeProvider(provider)
		if cfg.Provider == "" {
			return fmt.Errorf("--provider deve ser deepseek ou openrouter")
		}
	}
	if clearToken {
		cfg.APIKey = ""
	}
	if language != "" {
		cfg.DefaultLanguage = normalizeLanguage(language)
		if cfg.DefaultLanguage == "" {
			return fmt.Errorf("idioma padrão inválido")
		}
	}
	if source != "" {
		cfg.SourceLanguages = langutil.ParseOrdered(source)
		if len(cfg.SourceLanguages) == 0 {
			return fmt.Errorf("--source precisa ter ao menos um idioma ou auto")
		}
		cfg.SourceLanguage = ""
	}
	if model != "" {
		cfg.Model = strings.TrimSpace(model)
	}
	if batchSize != 0 {
		if batchSize < 1 {
			return fmt.Errorf("--batch deve ser maior que zero")
		}
		cfg.BatchSize = batchSize
	}
	if parallelism != 0 {
		if parallelism < 1 || parallelism > 8 {
			return fmt.Errorf("--parallel deve estar entre 1 e 8")
		}
		cfg.Parallelism = parallelism
	}
	if timeout != 0 {
		if timeout < time.Minute {
			return fmt.Errorf("--timeout deve ser de pelo menos 1m")
		}
		cfg.TimeoutSeconds = int(timeout / time.Second)
	}
	if retries != 0 {
		if retries < 0 {
			return fmt.Errorf("--retries não pode ser negativo")
		}
		cfg.Retries = retries
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "✓ configuração salva em %s\n  provedor: %s\n  idioma padrão: %s\n  origens: %s\n  chave DeepSeek: %s\n  chave OpenRouter: %s\n", path, providerLabel(savedProvider(cfg)), valueOr(cfg.DefaultLanguage, "não configurado"), langutil.FormatOrdered(savedSourceLanguages(cfg)), maskedToken(cfg.DeepSeekAPIKey), maskedToken(cfg.APIKey))
	return nil
}

func runInspect(ctx context.Context, service app.Service, args []string, stderr io.Writer) error {
	args = allowPathFirst(args)
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	recursive := true
	fs.BoolVar(&recursive, "recursive", true, "buscar em subpastas")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("informe exatamente um arquivo ou pasta")
	}
	return service.Inspect(ctx, fs.Arg(0), recursive)
}

// The standard flag package stops at the first positional argument. Moving a
// leading path to the end lets both friendly forms work:
// subgen translate ./series --to pt-BR and subgen translate --to pt-BR ./series.
func allowPathFirst(args []string) []string {
	if len(args) > 1 && !strings.HasPrefix(args[0], "-") {
		return append(append([]string(nil), args[1:]...), args[0])
	}
	return args
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func valueOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func positiveOr(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func savedTimeout(cfg config.Config) time.Duration {
	if cfg.TimeoutSeconds > 0 {
		return time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	return defaultTimeout
}

func savedProvider(cfg config.Config) string {
	if provider := normalizeProvider(cfg.Provider); provider != "" {
		return provider
	}
	if cfg.APIKey != "" && cfg.DeepSeekAPIKey == "" {
		return "openrouter"
	}
	return defaultProvider
}

func savedModel(cfg config.Config, provider string) string {
	if cfg.Model != "" {
		if provider == "deepseek" && strings.Contains(cfg.Model, "/") {
			return defaultModel
		}
		if provider == "openrouter" && !strings.Contains(cfg.Model, "/") {
			return defaultOpenRouterModel
		}
		return cfg.Model
	}
	if provider == "openrouter" {
		return defaultOpenRouterModel
	}
	return defaultModel
}

func normalizeProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "deepseek", "deepseek-direct", "direto":
		return "deepseek"
	case "openrouter", "open-router":
		return "openrouter"
	default:
		return ""
	}
}

func providerLabel(provider string) string {
	if provider == "deepseek" {
		return "DeepSeek direto"
	}
	return "OpenRouter"
}

func maskedToken(token string) string {
	if token == "" {
		return "não configurada"
	}
	if len(token) <= 8 {
		return "••••"
	}
	return token[:5] + "…" + token[len(token)-4:]
}

func savedSourceLanguages(cfg config.Config) []string {
	if len(cfg.SourceLanguages) > 0 {
		return append([]string(nil), cfg.SourceLanguages...)
	}
	if strings.TrimSpace(cfg.SourceLanguage) != "" {
		if parsed := langutil.ParseOrdered(cfg.SourceLanguage); len(parsed) > 0 {
			return parsed
		}
	}
	return []string{"auto"}
}

func firstTranslationSource(languages []string) string {
	for _, candidate := range languages {
		if candidate != "auto" {
			return candidate
		}
	}
	return "auto"
}

func normalizeLanguage(language string) string {
	language = strings.TrimSpace(strings.ReplaceAll(language, "_", "-"))
	if strings.EqualFold(language, "auto") {
		return "auto"
	}
	compact := strings.ToLower(strings.ReplaceAll(language, "-", ""))
	if len(compact) == 4 {
		for _, r := range compact {
			if !unicode.IsLetter(r) {
				return language
			}
		}
		return compact[:2] + "-" + strings.ToUpper(compact[2:])
	}
	return language
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `subgen — tradução inteligente de legendas

USO
  subgen translate <arquivo-ou-pasta> --to <idioma> [opções]
  subgen info <arquivo-ou-pasta>
  subgen inspect <vídeo-ou-pasta>
  subgen config

EXEMPLOS
  subgen translate filme.srt --to pt-BR --dry-run
  subgen translate ./temporada --to pt-BR
  subgen info ./biblioteca
  subgen inspect filme.mkv
  subgen translate filme.mkv --to pt-BR --track 2
  subgen config

CONFIGURAÇÃO
  'subgen config' abre um assistente guiado e salva tudo com acesso restrito.
  DEEPSEEK_API_KEY     sobrescreve temporariamente a chave DeepSeek
  OPENROUTER_API_KEY   sobrescreve temporariamente a chave salva
  SUBGEN_PROVIDER      deepseek ou openrouter
  SUBGEN_LANGUAGE      sobrescreve temporariamente o idioma salvo
  SUBGEN_MODEL         modelo no provedor escolhido

O original nunca é alterado. As traduções são criadas ao lado do arquivo,
seguindo o padrão Plex/Jellyfin: filme.mkv gera filme.pt.srt.
`)
}
