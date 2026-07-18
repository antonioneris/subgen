package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/antonioneris/subgen/internal/language"
	"github.com/antonioneris/subgen/internal/media"
	"github.com/antonioneris/subgen/internal/openrouter"
	"github.com/antonioneris/subgen/internal/subtitle"
	terminalui "github.com/antonioneris/subgen/internal/ui"
)

type Options struct {
	Source, Target                    string
	SourceLanguages                   []string
	Provider, Model, APIKey, Endpoint string
	BatchSize, Retries, Parallelism   int
	Track                             int
	Timeout                           time.Duration
	Recursive, Overwrite, DryRun      bool
	NormalizeEffects                  bool
	SelectTrack                       func(context.Context, string, []media.SubtitleTrack) (int, error)
	HTTPClient                        *http.Client
}

type Service struct{ Out, Err io.Writer }

var mediaExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".mov": true, ".avi": true, ".webm": true, ".ts": true,
}

func (s Service) Translate(ctx context.Context, path string, opts Options) error {
	styles := terminalui.New(s.Out)
	if opts.Target == "" {
		return fmt.Errorf("idioma de destino obrigatório (use --to)")
	}
	if opts.BatchSize < 0 {
		return fmt.Errorf("--batch não pode ser negativo")
	}
	if opts.Parallelism == 0 {
		opts.Parallelism = 4
	}
	if opts.Parallelism < 1 || opts.Parallelism > 8 {
		return fmt.Errorf("--parallel deve estar entre 1 e 8")
	}
	files, err := discover(path, opts.Recursive)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("nenhuma legenda .srt ou mídia compatível encontrada em %q", path)
	}
	files, ignoredAuxiliary := libraryTranslationInputs(files)
	if ignoredAuxiliary > 0 {
		fmt.Fprintf(s.Out, "%s\n", styles.Muted.Render(fmt.Sprintf("↷ %d arquivo(s) .srt auxiliar(es) ignorado(s); em pastas com mídia, o subgen processa somente os vídeos", ignoredAuxiliary)))
	}
	mediaBases := make(map[string]bool)
	for _, discovered := range files {
		if mediaExtensions[strings.ToLower(filepath.Ext(discovered))] {
			mediaBases[strings.TrimSuffix(discovered, filepath.Ext(discovered))] = true
		}
	}

	var retryMu sync.Mutex
	client := &openrouter.Client{
		APIKey: opts.APIKey, Model: opts.Model, ProviderName: opts.Provider, Endpoint: opts.Endpoint,
		HTTP: opts.HTTPClient, Retries: opts.Retries, AppTitle: "subgen", Timeout: opts.Timeout,
		OnRetry: func(attempt int, delay time.Duration, reason string) {
			retryMu.Lock()
			defer retryMu.Unlock()
			description := "falha temporária: " + reason
			if strings.HasPrefix(reason, "resposta inválida:") {
				description = reason
			}
			fmt.Fprintf(s.Err, "  %s\n", styles.Warning.Render(fmt.Sprintf("↻ %s; nova tentativa %d em %s", description, attempt, delay.Round(time.Second))))
		},
	}
	var completed, skipped int
	var failures []error
	var totalUsage openrouter.Usage
	recordFailure := func(failure error) {
		failures = append(failures, failure)
		fmt.Fprintf(s.Err, "%s %s\n", styles.Error.Render("✗ Falha"), failure)
	}
	preferences := make(map[string]*trackPreference)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := os.Stat(file); os.IsNotExist(err) {
			// A legacy sidecar may have been migrated earlier in this same folder
			// pass; the discovery list intentionally remains immutable.
			continue
		}
		ext := strings.ToLower(filepath.Ext(file))
		if ext == ".srt" {
			// A same-name SRT next to a discovered video is its player sidecar,
			// never a new source to translate during folder processing.
			if mediaBases[strings.TrimSuffix(file, filepath.Ext(file))] {
				skipped++
				continue
			}
			if isTargetSidecar(file, opts.Target) {
				skipped++
				continue
			}
			output := sidecarName(file, opts.Target, -1)
			ok, usage, err := s.translateSRT(ctx, client, file, output, file, opts)
			totalUsage.Add(usage)
			if err != nil {
				failure := fmt.Errorf("%s: %w", file, err)
				if len(files) == 1 {
					return failure
				}
				failures = append(failures, failure)
				fmt.Fprintf(s.Err, "%s %s\n", styles.Error.Render("✗ Falha"), failure)
				continue
			}
			if ok {
				completed++
			} else {
				skipped++
			}
			continue
		}
		output := sidecarName(file, opts.Target, 0)
		if !opts.Overwrite {
			if _, err := os.Stat(output); err == nil {
				fmt.Fprintf(s.Out, "✓ já existe: %s\n", output)
				skipped++
				continue
			}
			regionalLegacy := regionalSidecarName(file, opts.Target)
			if regionalLegacy != output {
				if _, err := os.Stat(regionalLegacy); err == nil {
					if opts.DryRun {
						fmt.Fprintf(s.Out, "• renomearia para compatibilidade com Plex/Jellyfin: %s → %s\n", regionalLegacy, output)
					} else if err := migrateSidecar(regionalLegacy, output); err != nil {
						return err
					} else {
						fmt.Fprintf(s.Out, "✓ legenda identificada como %s: %s\n", opts.Target, output)
					}
					completed++
					continue
				}
			}
			plainLegacy := plainSidecarName(file)
			if _, err := os.Stat(plainLegacy); err == nil {
				if opts.DryRun {
					fmt.Fprintf(s.Out, "• renomearia para informar o idioma ao Jellyfin: %s → %s\n", plainLegacy, output)
				} else if err := migrateSidecar(plainLegacy, output); err != nil {
					return err
				} else {
					fmt.Fprintf(s.Out, "✓ legenda identificada como %s: %s\n", opts.Target, output)
				}
				completed++
				continue
			}
		}
		tracks, err := media.Probe(ctx, file)
		if err != nil {
			failure := fmt.Errorf("%s: %w", file, err)
			if len(files) == 1 {
				return failure
			}
			recordFailure(failure)
			continue
		}
		if len(tracks) == 0 {
			fmt.Fprintf(s.Err, "↷ %s: sem legendas embutidas\n", file)
			skipped++
			continue
		}
		if len(textSubtitleTracks(tracks)) == 0 {
			message := fmt.Sprintf("%s: só possui legendas gráficas; OCR necessário", file)
			if len(files) == 1 {
				return errors.New(message)
			}
			fmt.Fprintf(s.Err, "%s\n", styles.Warning.Render("↷ "+message))
			skipped++
			continue
		}
		preferenceKey := filepath.Dir(file)
		track, err := chooseTrack(ctx, file, tracks, opts, preferences[preferenceKey])
		if err != nil {
			failure := fmt.Errorf("%s: %w", file, err)
			if len(files) == 1 {
				return failure
			}
			recordFailure(failure)
			continue
		}
		if opts.Track < 0 {
			preferences[preferenceKey] = rememberTrackPreference(preferences[preferenceKey], track, len(textSubtitleTracks(tracks)))
		}
		priority, total := sourcePriority(track.Language, opts.SourceLanguages)
		if priority > 0 || len(opts.SourceLanguages) > 0 {
			detail := fmt.Sprintf("%s · automática · %s", mediaDisplayTitle(file), language.Canonical(track.Language))
			if priority > 0 {
				detail += fmt.Sprintf(" · prioridade %d/%d", priority, total)
			} else {
				detail += " · fallback fora das prioridades"
			}
			if track.Title != "" {
				detail += " · " + track.Title
			}
			fmt.Fprintln(s.Out, styles.Accent.Render(detail))
		}
		if isBitmapSubtitle(track.Codec) {
			message := fmt.Sprintf("%s faixa %d (%s): legenda gráfica requer OCR", file, track.Index, track.Codec)
			if len(files) == 1 {
				return errors.New(message)
			}
			fmt.Fprintf(s.Err, "%s\n", styles.Warning.Render("↷ "+message))
			skipped++
			continue
		}
		legacy := legacySidecarName(file, opts.Target, track.Index)
		if !opts.Overwrite {
			if _, err := os.Stat(legacy); err == nil {
				if opts.DryRun {
					fmt.Fprintf(s.Out, "• renomearia a tradução existente: %s → %s\n", legacy, output)
				} else if err := migrateSidecar(legacy, output); err != nil {
					return err
				} else {
					fmt.Fprintf(s.Out, "✓ tradução existente renomeada para o player: %s\n", output)
				}
				completed++
				continue
			}
		}
		if opts.DryRun {
			fmt.Fprintf(s.Out, "• traduziria somente a faixa %d de %s → %s\n", track.Index, file, output)
			completed++
			continue
		}
		tmp, err := os.CreateTemp("", "subgen-extracted-*.srt")
		if err != nil {
			failure := fmt.Errorf("%s: criar arquivo temporário: %w", file, err)
			if len(files) == 1 {
				return failure
			}
			recordFailure(failure)
			continue
		}
		tmpPath := tmp.Name()
		_ = tmp.Close()
		defer os.Remove(tmpPath)
		if err := media.ExtractSRT(ctx, file, track.Index, tmpPath); err != nil {
			_ = os.Remove(tmpPath)
			failure := fmt.Errorf("%s faixa %d: %w", file, track.Index, err)
			if len(files) == 1 {
				return failure
			}
			recordFailure(failure)
			continue
		}
		fileOpts := opts
		fileOpts.NormalizeEffects = true
		if detected := language.Canonical(track.Language); detected != "" && priority > 0 {
			fileOpts.Source = detected
		} else {
			fileOpts.Source = "auto"
		}
		displayName := fmt.Sprintf("%s · faixa %d (%s → %s)", file, track.Index, fileOpts.Source, fileOpts.Target)
		ok, usage, err := s.translateSRT(ctx, client, tmpPath, output, displayName, fileOpts)
		totalUsage.Add(usage)
		if err != nil {
			failure := fmt.Errorf("%s faixa %d: %w", file, track.Index, err)
			if len(files) == 1 {
				return failure
			}
			failures = append(failures, failure)
			fmt.Fprintf(s.Err, "%s %s\n", styles.Error.Render("✗ Falha"), failure)
			continue
		}
		if ok {
			completed++
		} else {
			skipped++
		}
	}
	if len(failures) > 0 {
		fmt.Fprintf(s.Out, "\n%s\n", styles.Warning.Render(fmt.Sprintf("⚠ Concluído: %d saída(s), %d ignorada(s), %d falha(s)", completed, skipped, len(failures))))
		printTotalUsage(s.Out, styles, totalUsage)
		return fmt.Errorf("%d arquivo(s) falharam; primeira falha: %w", len(failures), failures[0])
	}
	fmt.Fprintf(s.Out, "\n%s\n", styles.Success.Render(fmt.Sprintf("✓ Concluído: %d saída(s), %d ignorada(s)", completed, skipped)))
	printTotalUsage(s.Out, styles, totalUsage)
	return nil
}

type trackPreference struct {
	Index           int
	Language, Title string
}

// rememberTrackPreference keeps the user's choice across a folder. An episode
// with only one textual track is not a choice and must not replace it.
func rememberTrackPreference(current *trackPreference, selected media.SubtitleTrack, candidateCount int) *trackPreference {
	if candidateCount <= 1 {
		return current
	}
	return &trackPreference{Index: selected.Index, Language: selected.Language, Title: selected.Title}
}

func chooseTrack(ctx context.Context, path string, tracks []media.SubtitleTrack, opts Options, preferred *trackPreference) (media.SubtitleTrack, error) {
	if opts.Track >= 0 {
		for _, track := range tracks {
			if track.Index == opts.Track {
				return track, nil
			}
		}
		return media.SubtitleTrack{}, fmt.Errorf("%s não possui a faixa de legenda %d", path, opts.Track)
	}
	textTracks := textSubtitleTracks(tracks)
	if len(textTracks) == 0 {
		return media.SubtitleTrack{}, fmt.Errorf("%s só possui legendas gráficas; OCR necessário", path)
	}
	sourceLanguages := opts.SourceLanguages
	if len(sourceLanguages) == 0 && !strings.EqualFold(strings.TrimSpace(opts.Source), "auto") && strings.TrimSpace(opts.Source) != "" {
		sourceLanguages = []string{language.Canonical(opts.Source)}
	}
	useAutomaticFallback := len(sourceLanguages) > 0
	for _, preferredLanguage := range sourceLanguages {
		preferredLanguage = language.Canonical(preferredLanguage)
		if preferredLanguage == "auto" {
			continue
		}
		var matches []media.SubtitleTrack
		for _, track := range textTracks {
			if language.Canonical(track.Language) == preferredLanguage && !isForcedTrack(track) {
				matches = append(matches, track)
			}
		}
		if len(matches) > 0 {
			return bestSourceTrack(matches), nil
		}
	}
	if useAutomaticFallback {
		// The ordered list is a preference, not a hard filter. Translation
		// models can detect the source language, so never discard a usable
		// textual subtitle merely because its metadata is outside the list.
		complete := make([]media.SubtitleTrack, 0, len(textTracks))
		for _, track := range textTracks {
			if !isForcedTrack(track) {
				complete = append(complete, track)
			}
		}
		if len(complete) > 0 {
			return bestSourceTrack(complete), nil
		}
		return bestSourceTrack(textTracks), nil
	}
	if preferred != nil {
		for _, track := range textTracks {
			if track.Index == preferred.Index && (preferred.Language == "" || track.Language == preferred.Language) {
				return track, nil
			}
		}
		var matches []media.SubtitleTrack
		for _, track := range textTracks {
			if compatibleTrack(track, *preferred) {
				matches = append(matches, track)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
	}
	if len(textTracks) == 1 {
		return textTracks[0], nil
	}
	if opts.SelectTrack == nil {
		return media.SubtitleTrack{}, fmt.Errorf("%s possui %d faixas textuais; selecione uma com --track", path, len(textTracks))
	}
	index, err := opts.SelectTrack(ctx, path, textTracks)
	if err != nil {
		return media.SubtitleTrack{}, err
	}
	for _, track := range textTracks {
		if track.Index == index {
			return track, nil
		}
	}
	return media.SubtitleTrack{}, fmt.Errorf("faixa selecionada %d não existe em %s", index, path)
}

func bestSourceTrack(tracks []media.SubtitleTrack) media.SubtitleTrack {
	best, bestScore := tracks[0], trackQuality(tracks[0])
	for _, track := range tracks[1:] {
		if score := trackQuality(track); score > bestScore {
			best, bestScore = track, score
		}
	}
	return best
}

func trackQuality(track media.SubtitleTrack) int {
	title := strings.ToLower(track.Title)
	score := 0
	if track.Default {
		score += 20
	}
	if strings.Contains(title, "full") || strings.Contains(title, "complete") || strings.Contains(title, "completi") {
		score += 100
	}
	if strings.Contains(title, "default") {
		score += 20
	}
	if track.HearingImpaired || strings.Contains(title, "sdh") || strings.Contains(title, "[cc]") || strings.Contains(title, "hearing") {
		score -= 100
	}
	if isForcedTrack(track) {
		score -= 1000
	}
	return score
}

func isForcedTrack(track media.SubtitleTrack) bool {
	title := strings.ToLower(track.Title)
	return track.Forced || strings.Contains(title, "forced") || strings.Contains(title, "forçados") || strings.Contains(title, "forzati")
}

func sourcePriority(trackLanguage string, preferences []string) (int, int) {
	target := language.Canonical(trackLanguage)
	position, total := 0, 0
	for _, preference := range preferences {
		canonical := language.Canonical(preference)
		if canonical == "auto" || canonical == "" {
			continue
		}
		total++
		if position == 0 && canonical == target {
			position = total
		}
	}
	return position, total
}

var (
	seasonDirectory = regexp.MustCompile(`(?i)^(season|temporada)\s*\d+$`)
	episodeSuffix   = regexp.MustCompile(`(?i)\s+-\s+S\d{1,2}E\d{1,3}.*$`)
	releaseYear     = regexp.MustCompile(`(?i)[\s.\[(]+(?:19|20)\d{2}[\s.\])].*$`)
)

func mediaDisplayTitle(path string) string {
	title := filepath.Base(filepath.Dir(path))
	if title == "." || title == string(filepath.Separator) || seasonDirectory.MatchString(title) {
		title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if match := episodeSuffix.FindStringIndex(title); match != nil {
		title = title[:match[0]]
	}
	if match := releaseYear.FindStringIndex(title); match != nil && match[0] > 0 {
		title = title[:match[0]]
	}
	if !strings.Contains(title, " ") {
		title = strings.ReplaceAll(title, ".", " ")
	}
	title = strings.TrimSpace(strings.Trim(title, ".-_"))
	if title == "" {
		return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return title
}

func textSubtitleTracks(tracks []media.SubtitleTrack) []media.SubtitleTrack {
	result := make([]media.SubtitleTrack, 0, len(tracks))
	for _, track := range tracks {
		if !isBitmapSubtitle(track.Codec) {
			result = append(result, track)
		}
	}
	return result
}

func compatibleTrack(track media.SubtitleTrack, preferred trackPreference) bool {
	if preferred.Language != "" && track.Language != preferred.Language {
		return false
	}
	if preferred.Title != "" && track.Title != preferred.Title {
		return false
	}
	return true
}

func (s Service) translateSRT(ctx context.Context, client *openrouter.Client, input, output, displayName string, opts Options) (bool, openrouter.Usage, error) {
	styles := terminalui.New(s.Out)
	if !opts.Overwrite {
		if _, err := os.Stat(output); err == nil {
			fmt.Fprintf(s.Out, "✓ já existe: %s\n", output)
			return false, openrouter.Usage{}, nil
		}
	}
	f, err := os.Open(input)
	if err != nil {
		return false, openrouter.Usage{}, err
	}
	cues, err := subtitle.ParseSRT(f)
	_ = f.Close()
	if err != nil {
		return false, openrouter.Usage{}, err
	}
	if opts.NormalizeEffects {
		normalized, stats := subtitle.NormalizeForTranslation(cues)
		if len(normalized) == 0 {
			return false, openrouter.Usage{}, fmt.Errorf("a faixa contém apenas efeitos visuais ASS, sem texto traduzível")
		}
		cues = normalized
		if stats.Original != stats.Result {
			fmt.Fprintf(s.Out, "  %s\n", styles.Muted.Render(fmt.Sprintf("limpeza ASS: %d → %d blocos · %d desenhos removidos · %d efeitos repetidos consolidados", stats.Original, stats.Result, stats.RemovedDrawings, stats.Merged)))
		}
	}
	if opts.DryRun {
		planned := planBatches(cues, opts.BatchSize, opts.Parallelism)
		fmt.Fprintf(s.Out, "• traduziria %d blocos em %d chamada(s), até %d em paralelo: %s → %s\n", len(cues), len(planned), min(len(planned), opts.Parallelism), input, output)
		return true, openrouter.Usage{}, nil
	}

	batches := planBatches(cues, opts.BatchSize, opts.Parallelism)
	activeWorkers := min(len(batches), opts.Parallelism)
	fmt.Fprintf(s.Out, "%s\n", styles.Title.Render("◆ "+displayName))
	fmt.Fprintf(s.Out, "  %s\n", styles.Muted.Render(fmt.Sprintf("%d legendas · ~%s tokens de entrada · %d chamada(s) · %d em paralelo · limite sem dados %s", len(cues), compactNumber(estimateTokens(cues)), len(batches), activeWorkers, opts.Timeout)))
	translated := append([]subtitle.Cue(nil), cues...)
	started := time.Now()
	var progressMu sync.Mutex
	var firstData time.Duration
	receivedByBatch := make([]int, len(batches))
	completedByBatch := make([]int, len(batches))
	var translationUsage openrouter.Usage
	title := styles.Accent.Render(fmt.Sprintf("traduzindo com %s usando %d fluxo(s) paralelo(s)", opts.Model, activeWorkers))
	type batchResult struct {
		usage openrouter.Usage
		err   error
	}
	translateErr := terminalui.RunTask(ctx, s.Out, title, len(cues), func(report func(int, int)) error {
		workCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		semaphore := make(chan struct{}, opts.Parallelism)
		results := make(chan batchResult, len(batches))
		for index, batch := range batches {
			go func(index int, batch cueBatch) {
				select {
				case semaphore <- struct{}{}:
					defer func() { <-semaphore }()
				case <-workCtx.Done():
					results <- batchResult{err: workCtx.Err()}
					return
				}
				jobClient := *client
				jobClient.OnProgress = func(progress openrouter.StreamProgress) {
					progressMu.Lock()
					if firstData == 0 && progress.ReceivedBytes > 0 {
						firstData = time.Since(started)
					}
					receivedByBatch[index] = progress.ReceivedBytes
					completedByBatch[index] = min(progress.CompletedItems, batch.end-batch.start)
					totalReceived, totalCompleted := 0, 0
					for _, received := range receivedByBatch {
						totalReceived += received
					}
					for _, completed := range completedByBatch {
						totalCompleted += completed
					}
					progressMu.Unlock()
					report(totalCompleted, totalReceived)
				}
				beforeStart := max(0, batch.start-parallelContextCues)
				afterEnd := min(len(cues), batch.end+parallelContextCues)
				translation, err := jobClient.TranslateWithContextUsage(workCtx, cues[batch.start:batch.end], cues[beforeStart:batch.start], cues[batch.end:afterEnd], opts.Source, opts.Target)
				if err == nil {
					for offset, text := range translation.Texts {
						translated[batch.start+offset].Text = text
					}
					progressMu.Lock()
					completedByBatch[index] = batch.end - batch.start
					totalReceived, totalCompleted := 0, 0
					for _, received := range receivedByBatch {
						totalReceived += received
					}
					for _, completed := range completedByBatch {
						totalCompleted += completed
					}
					progressMu.Unlock()
					report(totalCompleted, totalReceived)
				}
				if err != nil {
					err = fmt.Errorf("parte %d/%d: %w", index+1, len(batches), err)
				}
				results <- batchResult{usage: translation.Usage, err: err}
			}(index, batch)
		}
		var firstErr error
		for range batches {
			result := <-results
			translationUsage.Add(result.usage)
			if result.err != nil && firstErr == nil {
				firstErr = result.err
				cancel()
			}
		}
		return firstErr
	})
	if translateErr != nil {
		return false, translationUsage, translateErr
	}
	elapsed := time.Since(started)
	received := 0
	for _, amount := range receivedByBatch {
		received += amount
	}
	if received > 0 {
		rate := float64(received) / elapsed.Seconds() / 1000
		fmt.Fprintf(s.Out, "  %s\n", styles.Muted.Render(fmt.Sprintf("desempenho %s · primeiro trecho %s · total %s · %.1f kB/s", providerDisplayName(opts.Provider), compactDuration(firstData), compactDuration(elapsed), rate)))
	}
	if err := writeAtomic(output, translated); err != nil {
		return false, translationUsage, err
	}
	fmt.Fprintf(s.Out, "%s %s\n", styles.Success.Render("✓ Tradução salva"), styles.Muted.Render(output))
	printFileUsage(s.Out, styles, opts.Provider, translationUsage)
	return true, translationUsage, nil
}

func printFileUsage(out io.Writer, styles terminalui.Styles, provider string, usage openrouter.Usage) {
	providerName := providerDisplayName(provider)
	if usage.CostKnown() {
		fmt.Fprintf(out, "  %s\n", styles.Muted.Render(fmt.Sprintf("custo %s: %s · %d tokens de entrada · %d de saída", providerName, formatUSD(usage.CostUSD), usage.PromptTokens, usage.CompletionTokens)))
		return
	}
	fmt.Fprintf(out, "  %s\n", styles.Muted.Render(fmt.Sprintf("custo %s: não informado pelo provedor · %d tokens de entrada · %d de saída", providerName, usage.PromptTokens, usage.CompletionTokens)))
}

func printTotalUsage(out io.Writer, styles terminalui.Styles, usage openrouter.Usage) {
	if usage.Requests == 0 {
		return
	}
	if usage.CostKnown() {
		fmt.Fprintf(out, "%s\n", styles.Accent.Render(fmt.Sprintf("$ Custo total desta execução: %s", formatUSD(usage.CostUSD))))
		return
	}
	fmt.Fprintf(out, "%s\n", styles.Muted.Render("$ Custo total: não informado pelo provedor"))
}

func formatUSD(value float64) string {
	if value > 0 && value < 0.000001 {
		return fmt.Sprintf("US$ %.8f", value)
	}
	return fmt.Sprintf("US$ %.6f", value)
}

func providerDisplayName(provider string) string {
	if strings.EqualFold(provider, openrouter.ProviderDeepSeek) {
		return "DeepSeek direto"
	}
	return "OpenRouter"
}

func compactDuration(duration time.Duration) string {
	if duration < time.Second {
		return duration.Round(10 * time.Millisecond).String()
	}
	return duration.Round(100 * time.Millisecond).String()
}

const parallelContextCues = 8
const parallelTargetTokens = 4_500
const automaticTokenBudget = 200_000

type cueBatch struct{ start, end int }

func planBatches(cues []subtitle.Cue, fixedSize, parallelism int) []cueBatch {
	if len(cues) == 0 {
		return nil
	}
	if fixedSize > 0 {
		var result []cueBatch
		for start := 0; start < len(cues); start += fixedSize {
			result = append(result, cueBatch{start, min(start+fixedSize, len(cues))})
		}
		return result
	}
	tokenLimit := automaticTokenBudget
	if parallelism > 1 {
		tokenLimit = parallelTargetTokens
	}
	var result []cueBatch
	start, tokens := 0, 0
	for i, cue := range cues {
		cueTokens := estimateCueTokens(cue)
		if i > start && tokens+cueTokens > tokenLimit {
			result = append(result, cueBatch{start, i})
			start, tokens = i, 0
		}
		tokens += cueTokens
	}
	return append(result, cueBatch{start, len(cues)})
}

func estimateTokens(cues []subtitle.Cue) int {
	total := 400
	for _, cue := range cues {
		total += estimateCueTokens(cue)
	}
	return total
}

func estimateCueTokens(cue subtitle.Cue) int { return len([]byte(cue.Text))/3 + 12 }

func compactNumber(value int) string {
	if value >= 1000 {
		return fmt.Sprintf("%.1fk", float64(value)/1000)
	}
	return fmt.Sprintf("%d", value)
}

func (s Service) Inspect(ctx context.Context, path string, recursive bool) error {
	files, err := discoverMedia(path, recursive)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("nenhuma mídia compatível encontrada em %q", path)
	}
	for _, file := range files {
		tracks, err := media.Probe(ctx, file)
		if err != nil {
			return err
		}
		fmt.Fprintf(s.Out, "%s\n", file)
		if len(tracks) == 0 {
			fmt.Fprintln(s.Out, "  sem legendas")
			continue
		}
		for _, tr := range tracks {
			lang := tr.Language
			if lang == "" {
				lang = "?"
			}
			extra := ""
			if tr.Title != "" {
				extra = " — " + tr.Title
			}
			kind := "texto"
			if isBitmapSubtitle(tr.Codec) {
				kind = "gráfica/OCR"
			}
			fmt.Fprintf(s.Out, "  faixa %-3d idioma=%-5s codec=%-20s %s%s\n", tr.Index, lang, tr.Codec, kind, extra)
		}
	}
	return nil
}

func discover(path string, recursive bool) ([]string, error) {
	return discoverWith(path, recursive, func(ext string) bool { return ext == ".srt" || mediaExtensions[ext] })
}

// libraryTranslationInputs prevents a recursive library scan from treating
// every language inside Subs/ as an independent translation job. A standalone
// SRT remains supported when passed directly or from an SRT-only directory.
func libraryTranslationInputs(files []string) ([]string, int) {
	mediaCount := 0
	for _, file := range files {
		if mediaExtensions[strings.ToLower(filepath.Ext(file))] {
			mediaCount++
		}
	}
	if mediaCount == 0 {
		return files, 0
	}
	mediaFiles := make([]string, 0, mediaCount)
	for _, file := range files {
		if mediaExtensions[strings.ToLower(filepath.Ext(file))] {
			mediaFiles = append(mediaFiles, file)
		}
	}
	return mediaFiles, len(files) - len(mediaFiles)
}
func discoverMedia(path string, recursive bool) ([]string, error) {
	return discoverWith(path, recursive, func(ext string) bool { return mediaExtensions[ext] })
}
func discoverWith(path string, recursive bool, accept func(string) bool) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if !accept(strings.ToLower(filepath.Ext(path))) {
			return nil, fmt.Errorf("tipo de arquivo não suportado: %s", path)
		}
		abs, _ := filepath.Abs(path)
		return []string{abs}, nil
	}
	var files []string
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && p != path && !recursive {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() || !accept(strings.ToLower(filepath.Ext(p))) {
			return nil
		}
		abs, _ := filepath.Abs(p)
		files = append(files, abs)
		return nil
	})
	sort.Strings(files)
	return files, err
}

func sidecarName(path, language string, track int) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	return fmt.Sprintf("%s.%s.srt", base, plexLanguageCode(language))
}

func regionalSidecarName(path, language string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	lang := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ' ' {
			return '-'
		}
		return r
	}, language)
	return fmt.Sprintf("%s.%s.srt", base, lang)
}

func plainSidecarName(path string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + ".srt"
}

func legacySidecarName(path, language string, track int) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	lang := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ' ' {
			return '-'
		}
		return r
	}, language)
	return fmt.Sprintf("%s.%s.track%d.srt", base, lang, track)
}

func migrateSidecar(oldPath, newPath string) error {
	source, err := os.Open(oldPath)
	if err != nil {
		return fmt.Errorf("abrir legenda antiga: %w", err)
	}
	defer source.Close()
	tmp, err := createReadableTemp(filepath.Dir(newPath))
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, source); err != nil {
		return fmt.Errorf("copiar legenda antiga: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, newPath); err != nil {
		return fmt.Errorf("publicar legenda migrada: %w", err)
	}
	committed = true
	if err := ensureSidecarReadable(newPath); err != nil {
		return err
	}
	if err := os.Remove(oldPath); err != nil {
		return fmt.Errorf("remover nome antigo da legenda: %w", err)
	}
	return nil
}
func isTargetSidecar(path, language string) bool {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	regional := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ' ' {
			return '-'
		}
		return r
	}, language)
	compatible := plexLanguageCode(language)
	tokens := strings.Split(strings.ToLower(stem), ".")
	regional = strings.ToLower(regional)
	for index, token := range tokens {
		if token != regional && token != compatible {
			continue
		}
		if index == len(tokens)-1 || sidecarQualifiers(tokens[index+1:]) {
			return true
		}
	}
	return false
}

func sidecarQualifiers(tokens []string) bool {
	for _, token := range tokens {
		switch token {
		case "hi", "sdh", "cc", "forced", "foreign", "signs", "songs", "default", "commentary":
			continue
		}
		if strings.HasPrefix(token, "track") {
			if _, err := strconv.Atoi(strings.TrimPrefix(token, "track")); err == nil {
				continue
			}
		}
		return false
	}
	return true
}

func plexLanguageCode(language string) string {
	language = strings.TrimSpace(language)
	language = strings.ReplaceAll(language, "_", "-")
	if base, _, found := strings.Cut(language, "-"); found && len(base) >= 2 {
		language = base
	}
	return strings.ToLower(language)
}
func isBitmapSubtitle(codec string) bool {
	switch codec {
	case "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle", "xsub":
		return true
	}
	return false
}
func writeAtomic(path string, cues []subtitle.Cue) error {
	dir := filepath.Dir(path)
	tmp, err := createReadableTemp(dir)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("ajustar permissão da legenda: %w", err)
	}
	if err := subtitle.WriteSRT(tmp, cues); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if err := ensureSidecarReadable(path); err != nil {
		return err
	}
	ok = true
	return nil
}

// os.CreateTemp always creates files as 0600. Some SMB servers ignore chmod,
// so that mode survives the final rename and media servers cannot read the
// subtitle. Create the temporary sidecar with a readable mode from the start.
func createReadableTemp(dir string) (*os.File, error) {
	for attempt := 0; attempt < 100; attempt++ {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, fmt.Errorf("gerar nome temporário: %w", err)
		}
		name := filepath.Join(dir, ".subgen-"+hex.EncodeToString(random[:])+".tmp")
		file, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
		if err == nil {
			return file, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("não foi possível criar arquivo temporário em %s", dir)
}

func ensureSidecarReadable(path string) error {
	if err := os.Chmod(path, 0o644); err != nil {
		return fmt.Errorf("ajustar permissão da legenda: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o044 == 0 {
		return fmt.Errorf("a legenda foi criada como %04o e não pode ser lida pelo Plex/Jellyfin; verifique as permissões do compartilhamento", info.Mode().Perm())
	}
	return nil
}
