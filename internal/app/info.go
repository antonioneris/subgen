package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/antonioneris/subgen/internal/language"
	"github.com/antonioneris/subgen/internal/media"
	"github.com/antonioneris/subgen/internal/openrouter"
	"github.com/antonioneris/subgen/internal/subtitle"
	terminalui "github.com/antonioneris/subgen/internal/ui"
)

type infoTotals struct {
	ready, pending, unavailable, failed int
	cues, promptTokens, outputTokens    int
	requests                            int
	costUSD                             float64
}

type infoResult struct {
	index  int
	row    terminalui.InfoRow
	totals infoTotals
}

var episodeCode = regexp.MustCompile(`(?i)S\d{1,2}E\d{1,3}`)

// Info performs a read-only inventory. It uses ffprobe and temporary subtitle
// extraction, but never calls a completion endpoint or writes beside the media.
func (s Service) Info(ctx context.Context, path string, opts Options) error {
	styles := terminalui.New(s.Out)
	if opts.Target == "" {
		return fmt.Errorf("idioma de destino obrigatório (use --to)")
	}
	if opts.Parallelism == 0 {
		opts.Parallelism = 4
	}
	files, err := discoverMedia(path, opts.Recursive)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("nenhuma mídia compatível encontrada em %q", path)
	}
	externalTargets := indexTargetSidecars(files, opts.Target)

	var pricing openrouter.ModelPricing
	pricingKnown := false
	var pricingWarning error
	// A fully covered library needs no cost forecast, so avoid even the small
	// pricing request in the most common repeated-scan case.
	if len(externalTargets) < len(files) && strings.EqualFold(opts.Provider, openrouter.ProviderOpenRouter) {
		pricing, pricingWarning = (&openrouter.Client{
			APIKey: opts.APIKey, Model: opts.Model, ProviderName: opts.Provider, HTTP: opts.HTTPClient,
		}).FetchModelPricing(ctx)
		pricingKnown = pricingWarning == nil
	}

	fmt.Fprintln(s.Out, styles.Title.Render("◆ SUBGEN · Diagnóstico da biblioteca"))
	fmt.Fprintf(s.Out, "  %s\n", styles.Muted.Render(fmt.Sprintf("%d mídia(s) · destino %s · origens %s · %s / %s", len(files), opts.Target, language.FormatOrdered(opts.SourceLanguages), providerDisplayName(opts.Provider), opts.Model)))

	rows := make([]terminalui.InfoRow, len(files))
	totals := infoTotals{}
	scanErr := terminalui.RunTask(ctx, s.Out, styles.Accent.Render("analisando mídias e legendas"), len(files), func(report func(int, int)) error {
		workCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		workers := min(max(opts.Parallelism, 1), min(len(files), 8))
		jobs := make(chan int)
		results := make(chan infoResult, workers)
		for range workers {
			go func() {
				for index := range jobs {
					file := files[index]
					row, result := inspectMediaForInfo(workCtx, file, opts, pricing, pricingKnown, externalTargets[file])
					results <- infoResult{index: index, row: row, totals: result}
				}
			}()
		}
		go func() {
			defer close(jobs)
			for index := range files {
				select {
				case jobs <- index:
				case <-workCtx.Done():
					return
				}
			}
		}()
		for completed := 1; completed <= len(files); completed++ {
			select {
			case result := <-results:
				rows[result.index] = result.row
				totals.add(result.totals)
				report(completed, 0)
			case <-workCtx.Done():
				return workCtx.Err()
			}
		}
		return nil
	})
	if scanErr != nil {
		return scanErr
	}

	fmt.Fprintln(s.Out)
	terminalui.RenderInfoTable(s.Out, rows)
	fmt.Fprintf(s.Out, "%s\n", styles.Success.Render(fmt.Sprintf("✓ %d pronta(s)", totals.ready)))
	fmt.Fprintf(s.Out, "%s\n", styles.Accent.Render(fmt.Sprintf("→ %d para traduzir · %d falas · ~%s tokens de entrada / ~%s de saída · %d chamada(s)", totals.pending, totals.cues, compactNumber(totals.promptTokens), compactNumber(totals.outputTokens), totals.requests)))
	if totals.unavailable > 0 || totals.failed > 0 {
		fmt.Fprintf(s.Out, "%s\n", styles.Warning.Render(fmt.Sprintf("↷ %d sem fonte configurada · %d com erro de leitura", totals.unavailable, totals.failed)))
	}
	if totals.pending == 0 {
		fmt.Fprintf(s.Out, "%s\n", styles.Muted.Render("$ Nenhum custo previsto: a biblioteca já está coberta"))
	} else if pricingKnown {
		fmt.Fprintf(s.Out, "%s\n", styles.Title.Render(fmt.Sprintf("$ Estimativa OpenRouter: %s", formatUSD(totals.costUSD))))
		fmt.Fprintf(s.Out, "  %s\n", styles.Muted.Render("estimativa; o total real varia com o tamanho da resposta e o provedor escolhido pelo roteamento"))
	} else if strings.EqualFold(opts.Provider, openrouter.ProviderOpenRouter) {
		fmt.Fprintf(s.Out, "%s\n", styles.Warning.Render("$ Custo indisponível: não foi possível consultar o preço atual do modelo no OpenRouter"))
		if pricingWarning != nil {
			fmt.Fprintf(s.Out, "  %s\n", styles.Muted.Render(pricingWarning.Error()))
		}
	} else {
		fmt.Fprintf(s.Out, "%s\n", styles.Muted.Render("$ Custo não estimado para a API DeepSeek direta; a previsão de tokens permanece válida"))
	}
	return nil
}

func (totals *infoTotals) add(other infoTotals) {
	totals.ready += other.ready
	totals.pending += other.pending
	totals.unavailable += other.unavailable
	totals.failed += other.failed
	totals.cues += other.cues
	totals.promptTokens += other.promptTokens
	totals.outputTokens += other.outputTokens
	totals.requests += other.requests
	totals.costUSD += other.costUSD
}

func inspectMediaForInfo(ctx context.Context, file string, opts Options, pricing openrouter.ModelPricing, pricingKnown bool, externalTarget string) (terminalui.InfoRow, infoTotals) {
	row := terminalui.InfoRow{Media: infoMediaName(file), Cues: "—", Tokens: "—", Calls: "—", Cost: "—"}
	if externalTarget != "" {
		row.Status, row.Source = "✓ pronta", externalTarget
		return row, infoTotals{ready: 1}
	}
	tracks, err := media.Probe(ctx, file)
	if err != nil {
		row.Status, row.Source = "! erro", shortInfoError(err)
		return row, infoTotals{failed: 1}
	}
	if found, source := embeddedTargetSubtitle(tracks, opts.Target); found {
		row.Status, row.Source = "✓ pronta", source
		return row, infoTotals{ready: 1}
	}
	track, err := chooseTrack(ctx, file, tracks, opts, nil)
	if err != nil {
		row.Status = "↷ sem fonte"
		var unavailable *sourceTrackUnavailableError
		if errors.As(err, &unavailable) {
			row.Source = language.FormatOrdered(unavailable.languages)
		} else {
			row.Source = shortInfoError(err)
		}
		return row, infoTotals{unavailable: 1}
	}
	cues, err := extractInfoCues(ctx, file, track.Index)
	if err != nil {
		row.Status, row.Source = "! erro", shortInfoError(err)
		return row, infoTotals{failed: 1}
	}
	cues, _ = subtitle.NormalizeForTranslation(cues)
	if len(cues) == 0 {
		row.Status, row.Source = "↷ sem falas", sourceTrackLabel(track)
		return row, infoTotals{unavailable: 1}
	}
	batches := planBatches(cues, opts.BatchSize, opts.Parallelism)
	promptTokens := estimatePlannedPromptTokens(cues, batches)
	outputTokens := estimateOutputTokens(cues)
	cost := 0.0
	if pricingKnown {
		cost = float64(promptTokens)*pricing.PromptPerToken + float64(outputTokens)*pricing.CompletionPerToken + float64(len(batches))*pricing.PerRequest
	}
	row.Status = "→ traduzir"
	row.Source = sourceTrackLabel(track)
	row.Cues = fmt.Sprintf("%d", len(cues))
	row.Tokens = compactNumber(promptTokens) + " / " + compactNumber(outputTokens)
	row.Calls = fmt.Sprintf("%d", len(batches))
	if pricingKnown {
		row.Cost = formatUSD(cost)
	} else {
		row.Cost = "indisponível"
	}
	return row, infoTotals{pending: 1, cues: len(cues), promptTokens: promptTokens, outputTokens: outputTokens, requests: len(batches), costUSD: cost}
}

func targetSubtitle(mediaPath string, tracks []media.SubtitleTrack, target string) (bool, string) {
	entries, _ := os.ReadDir(filepath.Dir(mediaPath))
	if found, source := externalTargetSubtitle(mediaPath, entries, target); found {
		return true, source
	}
	return embeddedTargetSubtitle(tracks, target)
}

func embeddedTargetSubtitle(tracks []media.SubtitleTrack, target string) (bool, string) {
	canonicalTarget := language.Canonical(target)
	for _, track := range tracks {
		if language.Canonical(track.Language) == canonicalTarget {
			return true, fmt.Sprintf("embutida · faixa %d · %s", track.Index, canonicalTarget)
		}
	}
	return false, ""
}

// indexTargetSidecars reads each media directory only once. On SMB/NFS this
// avoids one network round trip per video and lets covered media skip ffprobe.
func indexTargetSidecars(files []string, target string) map[string]string {
	byDirectory := make(map[string][]string)
	for _, file := range files {
		directory := filepath.Dir(file)
		byDirectory[directory] = append(byDirectory[directory], file)
	}
	found := make(map[string]string)
	for directory, mediaFiles := range byDirectory {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, file := range mediaFiles {
			if ok, source := externalTargetSubtitle(file, entries, target); ok {
				found[file] = source
			}
		}
	}
	return found
}

func externalTargetSubtitle(mediaPath string, entries []os.DirEntry, target string) (bool, string) {
	base := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	prefix := strings.ToLower(base) + "."
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(name), ".srt") || !strings.HasPrefix(strings.ToLower(name), prefix) {
			continue
		}
		if isTargetSidecar(name, target) {
			return true, "externa · " + plexLanguageCode(target)
		}
	}
	return false, ""
}

func extractInfoCues(ctx context.Context, file string, track int) ([]subtitle.Cue, error) {
	tmp, err := os.CreateTemp("", "subgen-info-*.srt")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(name)
	if err := media.ExtractSRT(ctx, file, track, name); err != nil {
		return nil, err
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return subtitle.ParseSRT(f)
}

func estimatePlannedPromptTokens(cues []subtitle.Cue, batches []cueBatch) int {
	total := 0
	for _, batch := range batches {
		start := max(0, batch.start-parallelContextCues)
		end := min(len(cues), batch.end+parallelContextCues)
		total += estimateTokens(cues[start:end])
	}
	return total
}

func estimateOutputTokens(cues []subtitle.Cue) int {
	total := 120
	for _, cue := range cues {
		total += len([]byte(cue.Text))/3 + 8
	}
	return total
}

func sourceTrackLabel(track media.SubtitleTrack) string {
	label := language.Canonical(track.Language)
	if label == "" {
		label = "?"
	}
	label += fmt.Sprintf(" · faixa %d", track.Index)
	if track.Title != "" {
		label += " · " + track.Title
	}
	return label
}

func infoMediaName(path string) string {
	title := mediaDisplayTitle(path)
	if code := episodeCode.FindString(filepath.Base(path)); code != "" {
		title += " · " + strings.ToUpper(code)
	}
	return title
}

func shortInfoError(err error) string {
	message := strings.ReplaceAll(err.Error(), "\n", " ")
	if len([]rune(message)) > 70 {
		return string([]rune(message)[:67]) + "…"
	}
	return message
}
