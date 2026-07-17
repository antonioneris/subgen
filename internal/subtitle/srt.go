package subtitle

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Cue is one subtitle entry. Timing is kept verbatim so translation can never
// change synchronization metadata.
type Cue struct {
	Index  int
	Timing string
	Text   string
}

var srtTiming = regexp.MustCompile(`^\d{1,}:\d{2}:\d{2}[,.]\d{3}\s+-->\s+\d{1,}:\d{2}:\d{2}[,.]\d{3}(?:\s+.*)?$`)
var fontTag = regexp.MustCompile(`(?i)</?font\b[^>]*>`)
var boldTag = regexp.MustCompile(`(?i)</?b\s*>`)
var anyTag = regexp.MustCompile(`<[^>]+>`)
var assOverride = regexp.MustCompile(`\{[^}]*\}`)
var vectorPathStart = regexp.MustCompile(`(?i)^[mnlbspc]\s+-?\d+(?:\.\d+)?(?:\s+-?\d+(?:\.\d+)?){1}`)

func ParseSRT(r io.Reader) ([]Cue, error) {
	scanner := bufio.NewScanner(r)
	// Subtitle lines can be surprisingly large (karaoke/ASS converted to SRT).
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var blocks [][]string
	var block []string
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if !utf8.ValidString(line) {
			return nil, fmt.Errorf("SRT não está em UTF-8")
		}
		if strings.TrimSpace(line) == "" {
			if len(block) > 0 {
				blocks = append(blocks, block)
				block = nil
			}
			continue
		}
		block = append(block, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ler SRT: %w", err)
	}
	if len(block) > 0 {
		blocks = append(blocks, block)
	}

	cues := make([]Cue, 0, len(blocks))
	seen := make(map[int]bool, len(blocks))
	for pos, lines := range blocks {
		if len(lines) < 2 {
			return nil, fmt.Errorf("bloco %d inválido: esperado índice e tempo", pos+1)
		}
		indexText := strings.TrimPrefix(strings.TrimSpace(lines[0]), "\ufeff")
		idx, err := strconv.Atoi(indexText)
		if err != nil {
			return nil, fmt.Errorf("bloco %d: índice %q inválido", pos+1, lines[0])
		}
		if !srtTiming.MatchString(strings.TrimSpace(lines[1])) {
			return nil, fmt.Errorf("legenda %d: tempo %q inválido", idx, lines[1])
		}
		if seen[idx] {
			return nil, fmt.Errorf("índice de legenda duplicado: %d", idx)
		}
		seen[idx] = true
		cues = append(cues, Cue{Index: idx, Timing: lines[1], Text: strings.Join(lines[2:], "\n")})
	}
	if len(cues) == 0 {
		return nil, fmt.Errorf("nenhuma legenda encontrada")
	}
	return cues, nil
}

func WriteSRT(w io.Writer, cues []Cue) error {
	bw := bufio.NewWriter(w)
	for _, cue := range cues {
		if _, err := fmt.Fprintf(bw, "%d\n%s\n%s\n\n", cue.Index, cue.Timing, cue.Text); err != nil {
			return fmt.Errorf("escrever SRT: %w", err)
		}
	}
	return bw.Flush()
}

type NormalizationStats struct {
	Original        int
	Result          int
	RemovedDrawings int
	RemovedEmpty    int
	Merged          int
}

// NormalizeForTranslation removes ASS rendering debris produced by FFmpeg's
// conversion to SRT. Animated signs can otherwise become thousands of
// near-identical cues and vector paths can consume most of the model output.
func NormalizeForTranslation(cues []Cue) ([]Cue, NormalizationStats) {
	stats := NormalizationStats{Original: len(cues)}
	result := make([]Cue, 0, len(cues))
	ends := make([]int64, 0, len(cues))
	lastByText := make(map[string]int)
	for _, cue := range cues {
		start, end, ok := timingMilliseconds(cue.Timing)
		if !ok {
			// ParseSRT already validates the syntax; preserve an unexpected timing
			// rather than dropping user content during cleanup.
			start, end = -1, -1
		}
		cleaned := cleanRenderedText(cue.Text)
		visible := strings.TrimSpace(anyTag.ReplaceAllString(cleaned, ""))
		visible = strings.Join(strings.Fields(visible), " ")
		if visible == "" {
			stats.RemovedEmpty++
			continue
		}
		if looksLikeVectorDrawing(visible) {
			stats.RemovedDrawings++
			continue
		}
		canonical := strings.ToLower(visible)
		if previousIndex, exists := lastByText[canonical]; exists && start >= 0 && start <= ends[previousIndex]+120 {
			if end > ends[previousIndex] {
				ends[previousIndex] = end
				previousStart, _, _ := timingMilliseconds(result[previousIndex].Timing)
				result[previousIndex].Timing = formatTiming(previousStart, end)
			}
			stats.Merged++
			continue
		}
		cue.Text = cleaned
		cue.Index = len(result) + 1
		result = append(result, cue)
		ends = append(ends, end)
		lastByText[canonical] = len(result) - 1
	}
	stats.Result = len(result)
	return result, stats
}

func cleanRenderedText(text string) string {
	text = fontTag.ReplaceAllString(text, "")
	text = boldTag.ReplaceAllString(text, "")
	text = assOverride.ReplaceAllString(text, "")
	lines := strings.Split(text, "\n")
	cleaned := lines[:0]
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, "\n")
}

func looksLikeVectorDrawing(text string) bool {
	if !vectorPathStart.MatchString(text) || len(text) < 80 {
		return false
	}
	digits, letters := 0, 0
	for _, char := range text {
		switch {
		case char >= '0' && char <= '9':
			digits++
		case char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z':
			letters++
		}
	}
	return digits > letters*2
}

func timingMilliseconds(timing string) (int64, int64, bool) {
	parts := strings.SplitN(timing, "-->", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, ok := timestampMilliseconds(strings.TrimSpace(parts[0]))
	if !ok {
		return 0, 0, false
	}
	endText := strings.Fields(strings.TrimSpace(parts[1]))
	if len(endText) == 0 {
		return 0, 0, false
	}
	end, ok := timestampMilliseconds(endText[0])
	return start, end, ok
}

func timestampMilliseconds(value string) (int64, bool) {
	value = strings.Replace(value, ".", ",", 1)
	var hours, minutes, seconds, millis int64
	if _, err := fmt.Sscanf(value, "%d:%d:%d,%d", &hours, &minutes, &seconds, &millis); err != nil {
		return 0, false
	}
	return ((hours*60+minutes)*60+seconds)*1000 + millis, true
}

func formatTiming(start, end int64) string {
	return formatTimestamp(start) + " --> " + formatTimestamp(end)
}

func formatTimestamp(value int64) string {
	if value < 0 {
		value = 0
	}
	hours := value / 3_600_000
	value %= 3_600_000
	minutes := value / 60_000
	value %= 60_000
	seconds := value / 1_000
	millis := value % 1_000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", hours, minutes, seconds, millis)
}
