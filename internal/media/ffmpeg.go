package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type SubtitleTrack struct {
	Index           int
	Codec           string
	Language        string
	Title           string
	Default         bool
	Forced          bool
	HearingImpaired bool
}

type probeOutput struct {
	Streams []struct {
		Index       int               `json:"index"`
		CodecName   string            `json:"codec_name"`
		CodecType   string            `json:"codec_type"`
		Tags        map[string]string `json:"tags"`
		Disposition struct {
			Default         int `json:"default"`
			Forced          int `json:"forced"`
			HearingImpaired int `json:"hearing_impaired"`
		} `json:"disposition"`
	} `json:"streams"`
}

func Probe(ctx context.Context, path string) ([]SubtitleTrack, error) {
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_streams", "-of", "json", path)
	out, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.Error); ok {
			return nil, fmt.Errorf("ffprobe não encontrado; instale FFmpeg")
		}
		return nil, fmt.Errorf("inspecionar %q: %w", path, err)
	}
	var data probeOutput
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("resposta inválida do ffprobe: %w", err)
	}
	var tracks []SubtitleTrack
	for _, s := range data.Streams {
		if s.CodecType != "subtitle" {
			continue
		}
		tracks = append(tracks, SubtitleTrack{
			Index: s.Index, Codec: s.CodecName, Language: s.Tags["language"], Title: s.Tags["title"],
			Default: s.Disposition.Default == 1, Forced: s.Disposition.Forced == 1, HearingImpaired: s.Disposition.HearingImpaired == 1,
		})
	}
	return tracks, nil
}

// ExtractSRT asks ffmpeg to convert a text subtitle stream to a temporary SRT.
// Bitmap formats such as PGS/VobSub intentionally fail: they require OCR.
func ExtractSRT(ctx context.Context, mediaPath string, streamIndex int, outputPath string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-y", "-i", mediaPath, "-map", "0:"+strconv.Itoa(streamIndex), "-c:s", "srt", outputPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.Error); ok {
			return fmt.Errorf("ffmpeg não encontrado; instale FFmpeg")
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("extrair faixa %d: %s", streamIndex, msg)
	}
	return nil
}
