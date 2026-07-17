package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/antonioneris/subgen/internal/media"
	"github.com/antonioneris/subgen/internal/subtitle"
)

func TestTranslateDryRunDirectorySkipsExistingTargetLanguage(t *testing.T) {
	dir := t.TempDir()
	srt := "1\n00:00:01,000 --> 00:00:02,000\nHello\n"
	if err := os.WriteFile(filepath.Join(dir, "episode.srt"), []byte(srt), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.pt-BR.srt"), []byte(srt), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	s := Service{Out: &out, Err: &out}
	err := s.Translate(context.Background(), dir, Options{Target: "pt-BR", Source: "auto", BatchSize: 0, Track: -1, Recursive: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out.String(), "traduziria 1 blocos em 1 chamada"); got != 1 {
		t.Fatalf("translations = %d, output=%q", got, out.String())
	}
}

func TestLibraryTranslationInputsIgnoreAuxiliarySRTsWhenMediaExists(t *testing.T) {
	files := []string{
		"/movies/Film/Film.mkv",
		"/movies/Film/Film.pt.srt",
		"/movies/Film/Subs/Arabic.ara.srt",
		"/movies/Film/Subs/English.eng.srt",
	}
	got, ignored := libraryTranslationInputs(files)
	if ignored != 3 || len(got) != 1 || got[0] != files[0] {
		t.Fatalf("got=%q ignored=%d", got, ignored)
	}

	standalone := []string{"/subtitles/a.srt", "/subtitles/b.srt"}
	got, ignored = libraryTranslationInputs(standalone)
	if ignored != 0 || len(got) != 2 {
		t.Fatalf("standalone got=%q ignored=%d", got, ignored)
	}
}

func TestTargetSubtitleFindsOnlySameMediaSidecarAndEmbeddedLanguage(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Episode.mkv")
	if err := os.WriteFile(filepath.Join(dir, "Other.pt.srt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, _ := targetSubtitle(mediaPath, nil, "pt-BR"); found {
		t.Fatal("sidecar from another media was accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "Episode.pt-BR.hi.srt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, source := targetSubtitle(mediaPath, nil, "pt-BR"); !found || !strings.Contains(source, "externa") {
		t.Fatalf("found=%v source=%q", found, source)
	}
	if err := os.Remove(filepath.Join(dir, "Episode.pt-BR.hi.srt")); err != nil {
		t.Fatal(err)
	}
	tracks := []media.SubtitleTrack{{Index: 4, Language: "por", Codec: "subrip"}}
	if found, source := targetSubtitle(mediaPath, tracks, "pt-BR"); !found || !strings.Contains(source, "faixa 4") {
		t.Fatalf("found=%v source=%q", found, source)
	}
}

func TestInfoTokenEstimateIncludesParallelContext(t *testing.T) {
	cues := make([]subtitle.Cue, 120)
	for index := range cues {
		cues[index] = subtitle.Cue{Index: index + 1, Text: strings.Repeat("dialogue ", 20)}
	}
	batches := planBatches(cues, 30, 4)
	if got, base := estimatePlannedPromptTokens(cues, batches), estimateTokens(cues); got <= base {
		t.Fatalf("planned=%d base=%d; expected repeated context overhead", got, base)
	}
	if output := estimateOutputTokens(cues); output <= 0 {
		t.Fatalf("output estimate = %d", output)
	}
}

func TestPlanBatchesUsesWholeSubtitleWhenParallelismIsDisabled(t *testing.T) {
	cues := make([]subtitle.Cue, 341)
	for i := range cues {
		cues[i] = subtitle.Cue{Index: i + 1, Text: "A normal subtitle line."}
	}
	batches := planBatches(cues, 0, 1)
	if len(batches) != 1 || batches[0] != (cueBatch{0, 341}) {
		t.Fatalf("batches = %#v", batches)
	}
}

func TestPlanBatchesHonorsExplicitManualLimit(t *testing.T) {
	batches := planBatches(make([]subtitle.Cue, 121), 60, 4)
	if len(batches) != 3 {
		t.Fatalf("batches = %#v", batches)
	}
}

func TestPlanBatchesCreatesParallelWorkForNormalEpisode(t *testing.T) {
	cues := make([]subtitle.Cue, 366)
	for i := range cues {
		cues[i] = subtitle.Cue{Index: i + 1, Text: strings.Repeat("fala ", 20)}
	}
	batches := planBatches(cues, 0, 4)
	if len(batches) < 3 {
		t.Fatalf("expected parallel batches, got %#v", batches)
	}
	if batches[0].start != 0 || batches[len(batches)-1].end != len(cues) {
		t.Fatalf("incomplete batches: %#v", batches)
	}
}

func TestTranslateRunsOpenRouterBatchesConcurrently(t *testing.T) {
	var active, maximum atomic.Int32
	httpClient := &http.Client{Transport: appRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			seen := maximum.Load()
			if current <= seen || maximum.CompareAndSwap(seen, current) {
				break
			}
		}
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return nil, err
		}
		var input struct {
			Subtitles []struct {
				ID int `json:"id"`
			} `json:"subtitles"`
		}
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
			t.Error(err)
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
		translations := make([]map[string]any, len(input.Subtitles))
		for i, item := range input.Subtitles {
			translations[i] = map[string]any{"id": item.ID, "text": fmt.Sprintf("traduzida %d", item.ID)}
		}
		content, _ := json.Marshal(map[string]any{"translations": translations})
		encoded, _ := json.Marshal(string(content))
		body := fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":50,\"total_tokens\":150,\"cost\":0.000321}}\n\ndata: [DONE]\n\n", encoded)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "episode.srt")
	var source strings.Builder
	for i := 1; i <= 180; i++ {
		fmt.Fprintf(&source, "%d\n00:00:%02d,000 --> 00:00:%02d,900\n%s\n\n", i, i%60, i%60, strings.Repeat("long dialogue ", 12))
	}
	if err := os.WriteFile(inputPath, []byte(source.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	service := Service{Out: &output, Err: &output}
	err := service.Translate(context.Background(), inputPath, Options{
		Target: "pt-BR", Source: "en", Provider: "openrouter", Model: "test/model",
		APIKey: "secret", Parallelism: 4, Retries: 0, HTTPClient: httpClient,
		Timeout: time.Second, Track: -1, Recursive: true, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if maximum.Load() < 2 {
		t.Fatalf("requests were serialized; max concurrency = %d", maximum.Load())
	}
	translated, err := os.ReadFile(filepath.Join(dir, "episode.pt.srt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(translated, []byte("traduzida 180")) {
		t.Fatalf("last cue missing from output")
	}
	if !strings.Contains(output.String(), "custo OpenRouter: US$") || !strings.Contains(output.String(), "Custo total desta execução: US$") {
		t.Fatalf("cost was not reported: %s", output.String())
	}
}

func TestTranslateDirectoryContinuesAfterOneInvalidModelResponse(t *testing.T) {
	var calls atomic.Int32
	httpClient := &http.Client{Transport: appRoundTripFunc(func(*http.Request) (*http.Response, error) {
		call := calls.Add(1)
		content := `{"translations":[{"id":1,"text":"Traduzida"}]}`
		if call == 1 {
			content = `{"translations":[{"id":2,"text":"Inventada"}]}`
		}
		encoded, _ := json.Marshal(content)
		body := fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\ndata: [DONE]\n\n", encoded)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	dir := t.TempDir()
	srt := []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")
	for _, name := range []string{"a.srt", "b.srt"} {
		if err := os.WriteFile(filepath.Join(dir, name), srt, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	service := Service{Out: &output, Err: &output}
	err := service.Translate(context.Background(), dir, Options{
		Target: "pt-BR", Source: "en", Provider: "openrouter", Model: "test/model", APIKey: "secret",
		Parallelism: 1, Retries: 0, HTTPClient: httpClient, Timeout: time.Second, Track: -1, Recursive: true,
	})
	if err == nil || !strings.Contains(err.Error(), "1 arquivo(s) falharam") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.pt.srt")); !os.IsNotExist(err) {
		t.Fatalf("invalid translation was saved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.pt.srt")); err != nil {
		t.Fatalf("second file was not translated: %v; output=%s", err, output.String())
	}
	if !strings.Contains(output.String(), "1 falha(s)") {
		t.Fatalf("missing failure summary: %s", output.String())
	}
}

type appRoundTripFunc func(*http.Request) (*http.Response, error)

func (function appRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestChooseTrackNeverSelectsAllImplicitly(t *testing.T) {
	tracks := []media.SubtitleTrack{{Index: 2, Language: "eng", Codec: "subrip"}, {Index: 3, Language: "por", Codec: "subrip"}}
	selectorCalls := 0
	opts := Options{Track: -1, SelectTrack: func(_ context.Context, _ string, got []media.SubtitleTrack) (int, error) {
		selectorCalls++
		if len(got) != 2 {
			t.Fatalf("tracks = %#v", got)
		}
		return 2, nil
	}}
	chosen, err := chooseTrack(context.Background(), "episode.mkv", tracks, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Index != 2 || selectorCalls != 1 {
		t.Fatalf("chosen=%#v calls=%d", chosen, selectorCalls)
	}
}

func TestChooseTrackUsesOrderedSourceFallbackAndPrefersFullTrack(t *testing.T) {
	tracks := []media.SubtitleTrack{
		{Index: 3, Language: "ita", Codec: "ass", Title: "Forced ITA"},
		{Index: 4, Language: "ita", Codec: "subrip", Title: "ITA Completi"},
		{Index: 5, Language: "eng", Codec: "subrip", Title: "Forced ENG", Forced: true},
		{Index: 6, Language: "eng", Codec: "subrip", Title: "ENG Full"},
	}
	selectorCalls := 0
	chosen, err := chooseTrack(context.Background(), "Enola Holmes 3.mkv", tracks, Options{
		Track: -1, SourceLanguages: []string{"en", "it"},
		SelectTrack: func(context.Context, string, []media.SubtitleTrack) (int, error) {
			selectorCalls++
			return 3, nil
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Index != 6 || selectorCalls != 0 {
		t.Fatalf("chosen=%#v selectorCalls=%d", chosen, selectorCalls)
	}

	chosen, err = chooseTrack(context.Background(), "Italian only.mkv", tracks[:2], Options{Track: -1, SourceLanguages: []string{"en", "it"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Index != 4 {
		t.Fatalf("Italian fallback chose %#v", chosen)
	}
}

func TestChooseTrackTreatsENMAsEnglishAndSkipsWhenFallbackMissing(t *testing.T) {
	chosen, err := chooseTrack(context.Background(), "episode.mkv", []media.SubtitleTrack{
		{Index: 2, Language: "enm", Codec: "ass", Title: "Honorific"},
		{Index: 3, Language: "jpn", Codec: "ass"},
	}, Options{Track: -1, SourceLanguages: []string{"en", "fr"}}, nil)
	if err != nil || chosen.Index != 2 {
		t.Fatalf("chosen=%#v error=%v", chosen, err)
	}

	_, err = chooseTrack(context.Background(), "Italian only.mkv", []media.SubtitleTrack{{Index: 3, Language: "ita", Codec: "ass"}}, Options{Track: -1, SourceLanguages: []string{"en", "fr"}}, nil)
	var unavailable *sourceTrackUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestChooseTrackDoesNotTreatForcedOnlyAsACompleteLanguageFallback(t *testing.T) {
	chosen, err := chooseTrack(context.Background(), "movie.mkv", []media.SubtitleTrack{
		{Index: 2, Language: "eng", Codec: "subrip", Title: "Forced ENG", Forced: true},
		{Index: 3, Language: "fre", Codec: "subrip", Title: "French Full"},
	}, Options{Track: -1, SourceLanguages: []string{"en", "fr"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Index != 3 {
		t.Fatalf("chosen = %#v", chosen)
	}
}

func TestMediaDisplayTitleRemovesReleaseMetadata(t *testing.T) {
	for path, want := range map[string]string{
		"/movies/Enola Holmes 3 (2026)/Enola Holmes 3 (2026) WEBDL-1080p.mkv":                                         "Enola Holmes 3",
		"/movies/A.House.Of.Dynamite.2025.1080p.WEBRip/A.House.Of.Dynamite.2025.1080p.WEBRip.mp4":                     "A House Of Dynamite",
		"/series/From Old Country Bumpkin/Season 2/From Old Country Bumpkin - S02E01 - Episode title WEBDL-1080p.mkv": "From Old Country Bumpkin",
		"/series/Adam's Sweet Agony/Adam's Sweet Agony - S01E03 - I stopped hiding it.mkv":                            "Adam's Sweet Agony",
	} {
		if got := mediaDisplayTitle(path); got != want {
			t.Errorf("mediaDisplayTitle(%q)=%q want %q", path, got, want)
		}
	}
}

func TestChooseTrackReusesSeasonPreference(t *testing.T) {
	tracks := []media.SubtitleTrack{{Index: 2, Language: "eng", Title: "English", Codec: "subrip"}, {Index: 3, Language: "spa", Codec: "subrip"}}
	preferred := &trackPreference{Index: 2, Language: "eng", Title: "English"}
	chosen, err := chooseTrack(context.Background(), "episode2.mkv", tracks, Options{Track: -1}, preferred)
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Index != 2 {
		t.Fatalf("chosen = %#v", chosen)
	}
}

func TestSingleTrackDoesNotReplaceSeasonPreference(t *testing.T) {
	preference := &trackPreference{Index: 2, Language: "enm", Title: "Honorific"}
	onlyTrack := media.SubtitleTrack{Index: 2, Language: "eng", Title: "Default", Codec: "ass"}
	preference = rememberTrackPreference(preference, onlyTrack, 1)
	if preference.Language != "enm" || preference.Title != "Honorific" {
		t.Fatalf("single track replaced preference: %#v", preference)
	}

	tracks := []media.SubtitleTrack{
		{Index: 2, Language: "enm", Title: "Honorific", Codec: "ass"},
		{Index: 3, Language: "eng", Title: "Non-Honorific", Codec: "ass"},
	}
	selectorCalls := 0
	chosen, err := chooseTrack(context.Background(), "episode5.mkv", tracks, Options{Track: -1, SelectTrack: func(context.Context, string, []media.SubtitleTrack) (int, error) {
		selectorCalls++
		return 3, nil
	}}, preference)
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Index != 2 || selectorCalls != 0 {
		t.Fatalf("chosen=%#v selectorCalls=%d", chosen, selectorCalls)
	}
}

func TestSidecarName(t *testing.T) {
	if got := sidecarName("/videos/movie.mkv", "pt-BR", 3); got != "/videos/movie.pt.srt" {
		t.Fatalf("got %q", got)
	}
	if got := sidecarName("/videos/source.srt", "pt-BR", -1); got != "/videos/source.pt.srt" {
		t.Fatalf("standalone = %q", got)
	}
	if got := regionalSidecarName("/videos/movie.mkv", "pt-BR"); got != "/videos/movie.pt-BR.srt" {
		t.Fatalf("regional legacy = %q", got)
	}
	if got := legacySidecarName("/videos/movie.mkv", "pt-BR", 3); got != "/videos/movie.pt-BR.track3.srt" {
		t.Fatalf("legacy = %q", got)
	}
	if got := plainSidecarName("/videos/movie.mkv"); got != "/videos/movie.srt" {
		t.Fatalf("plain = %q", got)
	}
}

func TestPlexLanguageCode(t *testing.T) {
	for input, want := range map[string]string{"pt-BR": "pt", "en_US": "en", "por": "por", "ja": "ja"} {
		if got := plexLanguageCode(input); got != want {
			t.Errorf("plexLanguageCode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTargetSidecarRecognizesLanguageBeforeQualifiers(t *testing.T) {
	for _, path := range []string{
		"Ballerina (2025).pt.srt",
		"Ballerina (2025).pt-BR.hi.srt",
		"Ballerina (2025).pt-BR.sdh.srt",
		"Ballerina (2025).pt.forced.srt",
		"Ballerina (2025).pt-BR.track5.srt",
	} {
		if !isTargetSidecar(path, "pt-BR") {
			t.Errorf("target sidecar not recognized: %s", path)
		}
	}
	if isTargetSidecar("documentary.pt.final-cut.srt", "pt-BR") {
		t.Fatal("ordinary filename suffix was treated as a sidecar qualifier")
	}
}

func TestWriteAtomicCreatesReadableSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "episode.pt-BR.srt")
	if err := writeAtomic(path, []subtitle.Cue{{Index: 1, Timing: "00:00:00,000 --> 00:00:01,000", Text: "Olá"}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}
}

func TestCreateReadableTempStartsReadable(t *testing.T) {
	file, err := createReadableTemp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o044 == 0 {
		t.Fatalf("temporary permissions = %o", info.Mode().Perm())
	}
}

func TestMigrateSidecarCopiesThenRemovesLegacy(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "episode.pt-BR.srt")
	newPath := filepath.Join(dir, "episode.pt.srt")
	want := []byte("1\n00:00:00,000 --> 00:00:01,000\nOlá\n")
	if err := os.WriteFile(oldPath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateSidecar(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("migrated content = %q", got)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("legacy file still exists: %v", err)
	}
}
