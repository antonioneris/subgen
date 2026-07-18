package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/antonioneris/subgen/internal/config"
	langutil "github.com/antonioneris/subgen/internal/language"
	terminalui "github.com/antonioneris/subgen/internal/ui"
	"golang.org/x/term"
)

type wizardPrompter struct {
	input  io.Reader
	reader *bufio.Reader
	out    io.Writer
}

func runConfigWizard(path string, stdin io.Reader, stdout io.Writer) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	p := wizardPrompter{input: stdin, reader: bufio.NewReader(stdin), out: stdout}
	styles := terminalui.New(stdout)

	fmt.Fprintln(stdout, styles.Title.Render("◆ SUBGEN  Configuração guiada"))
	fmt.Fprintf(stdout, "Arquivo: %s\n\n", path)
	fmt.Fprintln(stdout, "Pressione Enter para manter o valor mostrado.")
	fmt.Fprintln(stdout, "Na chave, digite '-' se quiser removê-la.")
	fmt.Fprintln(stdout)

	provider, err := p.choice("1/5  Provedor de IA", []choice{
		{Value: "deepseek", Label: "DeepSeek direto (recomendado para velocidade)"},
		{Value: "openrouter", Label: "OpenRouter (vários provedores e fallback)"},
	}, savedProvider(cfg))
	if err != nil {
		return err
	}
	cfg.Provider = provider

	tokenHint := "não configurada"
	currentToken := cfg.DeepSeekAPIKey
	providerName := "DeepSeek"
	if provider == "openrouter" {
		currentToken = cfg.APIKey
		providerName = "OpenRouter"
	}
	if currentToken != "" {
		tokenHint = maskedToken(currentToken) + " — Enter mantém"
	}
	token, err := p.password(fmt.Sprintf("2/5  Chave do %s [%s]: ", providerName, tokenHint))
	if err != nil {
		return err
	}
	if provider == "deepseek" {
		if token == "-" {
			cfg.DeepSeekAPIKey = ""
		} else if token != "" {
			cfg.DeepSeekAPIKey = token
		}
		if cfg.DeepSeekAPIKey == "" {
			return fmt.Errorf("a chave da DeepSeek é necessária para usar o provedor direto")
		}
	} else {
		if token == "-" {
			cfg.APIKey = ""
		} else if token != "" {
			cfg.APIKey = token
		}
		if cfg.APIKey == "" {
			return fmt.Errorf("a chave do OpenRouter é necessária para usar esse provedor")
		}
	}

	language, err := p.text("3/5  Idioma padrão de saída", valueOr(cfg.DefaultLanguage, defaultLanguage))
	if err != nil {
		return err
	}
	cfg.DefaultLanguage = normalizeLanguage(language)
	if cfg.DefaultLanguage == "" {
		return fmt.Errorf("idioma de saída não pode ficar vazio")
	}

	model, err := p.text("4/5  Modelo padrão", savedModel(cfg, provider))
	if err != nil {
		return err
	}
	cfg.Model = strings.TrimSpace(model)
	if cfg.Model == "" {
		return fmt.Errorf("modelo não pode ficar vazio")
	}

	advanced, err := p.yesNo("5/5  Configurar opções avançadas?", false)
	if err != nil {
		return err
	}
	if advanced {
		fmt.Fprintln(stdout, "\n     Idiomas de origem por prioridade")
		fmt.Fprintln(stdout, "     O primeiro disponível será usado; somente uma faixa será traduzida.")
		fmt.Fprintln(stdout, "     Se nenhum existir, a melhor faixa textual disponível será usada automaticamente.")
		fmt.Fprintln(stdout, "     Exemplo: en, fr, es  (use auto se não quiser definir prioridades)")
		source, err := p.text("     Ordem", strings.Join(savedSourceLanguages(cfg), ", "))
		if err != nil {
			return err
		}
		cfg.SourceLanguages = langutil.ParseOrdered(source)
		if len(cfg.SourceLanguages) == 0 {
			return fmt.Errorf("informe ao menos um idioma de origem ou auto")
		}
		cfg.SourceLanguage = ""
		timeoutMinutes, err := p.integer("     Limite sem receber dados (minutos)", int(savedTimeout(cfg)/time.Minute), 1)
		if err != nil {
			return err
		}
		cfg.TimeoutSeconds = timeoutMinutes * 60
		retries, err := p.integer("     Tentativas em falhas temporárias", positiveOr(cfg.Retries, defaultRetries), 1)
		if err != nil {
			return err
		}
		cfg.Retries = retries
		parallelism, err := p.integerRange("     Chamadas simultâneas", positiveOr(cfg.Parallelism, defaultParallelism), 1, 8)
		if err != nil {
			return err
		}
		cfg.Parallelism = parallelism
	}
	// Automatic mode sizes parts by tokens and executes them concurrently.
	cfg.BatchSize = 0

	fmt.Fprintln(stdout, "\nResumo")
	fmt.Fprintf(stdout, "  Provedor:    %s\n", providerLabel(cfg.Provider))
	if cfg.Provider == "deepseek" {
		fmt.Fprintf(stdout, "  Chave:       %s\n", maskedToken(cfg.DeepSeekAPIKey))
	} else {
		fmt.Fprintf(stdout, "  Chave:       %s\n", maskedToken(cfg.APIKey))
	}
	fmt.Fprintf(stdout, "  Saída:       %s\n", cfg.DefaultLanguage)
	fmt.Fprintf(stdout, "  Origens:     %s\n", langutil.FormatOrdered(savedSourceLanguages(cfg)))
	fmt.Fprintf(stdout, "  Modelo:      %s\n", cfg.Model)
	fmt.Fprintln(stdout, "  Estratégia:  divisão automática por tokens")
	fmt.Fprintf(stdout, "  Paralelo:    %d chamadas\n", positiveOr(cfg.Parallelism, defaultParallelism))
	fmt.Fprintf(stdout, "  Sem dados:   limite de %s\n", savedTimeout(cfg))
	fmt.Fprintf(stdout, "  Tentativas:  %d\n\n", positiveOr(cfg.Retries, defaultRetries))

	save, err := p.yesNo("Salvar esta configuração?", true)
	if err != nil {
		return err
	}
	if !save {
		fmt.Fprintln(stdout, "Configuração cancelada; nenhuma alteração foi salva.")
		return nil
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "\n✓ Configuração salva. O subgen está pronto para traduzir.")
	return nil
}

type choice struct {
	Value string
	Label string
}

func (p *wizardPrompter) choice(label string, choices []choice, current string) (string, error) {
	defaultIndex := 0
	for i, item := range choices {
		if item.Value == current {
			defaultIndex = i
		}
	}
	for {
		fmt.Fprintln(p.out, label)
		for i, item := range choices {
			marker := " "
			if i == defaultIndex {
				marker = "›"
			}
			fmt.Fprintf(p.out, "  %s %d. %s\n", marker, i+1, item.Label)
		}
		fmt.Fprintf(p.out, "Escolha [%d]: ", defaultIndex+1)
		value, err := p.line()
		if err != nil {
			return "", err
		}
		if value == "" {
			return choices[defaultIndex].Value, nil
		}
		index, err := strconv.Atoi(value)
		if err == nil && index >= 1 && index <= len(choices) {
			return choices[index-1].Value, nil
		}
		fmt.Fprintf(p.out, "     Escolha um número de 1 a %d.\n", len(choices))
	}
}

func (p *wizardPrompter) password(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	if file, ok := p.input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		value, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(p.out)
		if err != nil {
			return "", fmt.Errorf("ler chave com segurança: %w", err)
		}
		return strings.TrimSpace(string(value)), nil
	}
	return p.line()
}

func (p *wizardPrompter) text(label, current string) (string, error) {
	fmt.Fprintf(p.out, "%s [%s]: ", label, current)
	value, err := p.line()
	if err != nil {
		return "", err
	}
	if value == "" {
		return current, nil
	}
	return value, nil
}

func (p *wizardPrompter) integer(label string, current, minimum int) (int, error) {
	for {
		value, err := p.text(label, strconv.Itoa(current))
		if err != nil {
			return 0, err
		}
		number, err := strconv.Atoi(value)
		if err == nil && number >= minimum {
			return number, nil
		}
		fmt.Fprintf(p.out, "     Digite um número maior ou igual a %d.\n", minimum)
	}
}

func (p *wizardPrompter) integerRange(label string, current, minimum, maximum int) (int, error) {
	for {
		value, err := p.text(label, strconv.Itoa(current))
		if err != nil {
			return 0, err
		}
		number, err := strconv.Atoi(value)
		if err == nil && number >= minimum && number <= maximum {
			return number, nil
		}
		fmt.Fprintf(p.out, "     Digite um número entre %d e %d.\n", minimum, maximum)
	}
}

func (p *wizardPrompter) yesNo(label string, defaultYes bool) (bool, error) {
	hint := "s/N"
	if defaultYes {
		hint = "S/n"
	}
	for {
		fmt.Fprintf(p.out, "%s [%s]: ", label, hint)
		value, err := p.line()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(value) {
		case "":
			return defaultYes, nil
		case "s", "sim", "y", "yes":
			return true, nil
		case "n", "nao", "não", "no":
			return false, nil
		default:
			fmt.Fprintln(p.out, "     Responda com s ou n.")
		}
	}
}

func (p *wizardPrompter) line() (string, error) {
	line, err := p.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("ler resposta: %w", err)
	}
	if errors.Is(err, io.EOF) && line == "" {
		return "", fmt.Errorf("configuração interrompida antes de terminar")
	}
	return strings.TrimSpace(line), nil
}
