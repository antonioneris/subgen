package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/antonioneris/subgen/internal/subtitle"
)

func TestTranslateMapsIDsAndSendsSchema(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("auth = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["response_format"] == nil {
			t.Error("missing response_format")
		}
		if stream, ok := body["stream"].(bool); !ok || !stream {
			t.Error("streaming não foi ativado")
		}
		provider, ok := body["provider"].(map[string]any)
		if !ok || provider["sort"] != "throughput" {
			t.Errorf("provider = %#v", body["provider"])
		}
		return sseResponse(
			`{"translations":[{"id":2,"text":"Mundo"},`,
			`{"id":1,"text":"Olá"}]}`,
		), nil
	})}
	var progress StreamProgress
	c := Client{
		APIKey: "secret", Model: "test/model", HTTP: httpClient, Retries: 0,
		OnProgress: func(current StreamProgress) { progress = current },
	}
	result, err := c.TranslateWithContextUsage(context.Background(), []subtitle.Cue{{Index: 1, Text: "Hello"}, {Index: 2, Text: "World"}}, nil, nil, "en", "pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	got := result.Texts
	if got[0] != "Olá" || got[1] != "Mundo" {
		t.Fatalf("got %q", got)
	}
	if progress.ReceivedBytes == 0 || progress.CompletedItems != 2 {
		t.Fatalf("progresso = %#v", progress)
	}
	if !result.Usage.CostKnown() || result.Usage.CostUSD != 0.000123 || result.Usage.PromptTokens != 20 || result.Usage.CompletionTokens != 8 {
		t.Fatalf("usage = %#v", result.Usage)
	}
}

func TestFetchModelPricing(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.String() != "https://prices.test/model" {
			t.Fatalf("request = %s %s", r.Method, r.URL)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization = %q", got)
		}
		body := `{"data":{"id":"deepseek/deepseek-v4-flash","pricing":{"prompt":"0.0000001","completion":"0.0000002","request":"0.00001"}}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	pricing, err := (&Client{APIKey: "secret", Model: "ignored", PricingEndpoint: "https://prices.test/model", HTTP: httpClient}).FetchModelPricing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pricing.ModelID != "deepseek/deepseek-v4-flash" || pricing.PromptPerToken != 0.0000001 || pricing.CompletionPerToken != 0.0000002 || pricing.PerRequest != 0.00001 {
		t.Fatalf("pricing = %#v", pricing)
	}
}

func TestTranslateRejectsMissingCue(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse(`{"translations":[]}`), nil
	})}
	c := Client{APIKey: "x", Model: "x", HTTP: httpClient}
	_, err := c.Translate(context.Background(), []subtitle.Cue{{Index: 7, Text: "hello"}}, "auto", "pt")
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestCompletedTranslationItemsHandlesPartialJSONAndEscapedText(t *testing.T) {
	partial := `{"translations":[{"id":1,"text":"Use {chaves} e \\"aspas\\""},{"id":2,"text":"incompleta`
	if got := completedTranslationItems(partial); got != 1 {
		t.Fatalf("completed items = %d", got)
	}
	complete := partial + `"}]}`
	if got := completedTranslationItems(complete); got != 2 {
		t.Fatalf("completed items = %d", got)
	}
}

func TestTranslateUsesDirectDeepSeekProtocol(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != DeepSeekEndpoint {
			t.Errorf("endpoint = %s", r.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "deepseek-v4-flash" {
			t.Errorf("model = %#v", body["model"])
		}
		format, _ := body["response_format"].(map[string]any)
		if format["type"] != "json_object" {
			t.Errorf("response_format = %#v", format)
		}
		thinking, _ := body["thinking"].(map[string]any)
		if thinking["type"] != "disabled" {
			t.Errorf("thinking = %#v", thinking)
		}
		if body["provider"] != nil || body["reasoning"] != nil {
			t.Errorf("campos exclusivos do OpenRouter enviados: %#v", body)
		}
		return sseResponse(`{"translations":[{"id":1,"text":"Olá"}]}`), nil
	})}
	c := Client{APIKey: "deepseek-secret", Model: "deepseek-v4-flash", ProviderName: ProviderDeepSeek, HTTP: httpClient}
	got, err := c.Translate(context.Background(), []subtitle.Cue{{Index: 1, Text: "Hello"}}, "en", "pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "Olá" {
		t.Fatalf("got %q", got)
	}
}

func TestTranslateRetriesInterruptedResponseBody(t *testing.T) {
	calls, retries := 0, 0
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: errorBody{}}, nil
		}
		return sseResponse(`{"translations":[{"id":1,"text":"Olá"}]}`), nil
	})}
	c := Client{
		APIKey: "x", Model: "x", HTTP: httpClient, Retries: 1,
		Wait:    func(context.Context, time.Duration) error { return nil },
		OnRetry: func(int, time.Duration, string) { retries++ },
	}
	got, err := c.Translate(context.Background(), []subtitle.Cue{{Index: 1, Text: "Hello"}}, "en", "pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || retries != 1 || got[0] != "Olá" {
		t.Fatalf("calls=%d retries=%d got=%q", calls, retries, got)
	}
}

func TestTranslateRetriesInvalidIDsWithCorrection(t *testing.T) {
	calls, retries := 0, 0
	var retryReason string
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		format := body["response_format"].(map[string]any)
		jsonSchema := format["json_schema"].(map[string]any)
		root := jsonSchema["schema"].(map[string]any)
		translations := root["properties"].(map[string]any)["translations"].(map[string]any)
		if translations["minItems"] != float64(2) || translations["maxItems"] != float64(2) {
			t.Fatalf("array limits = %#v", translations)
		}
		id := translations["items"].(map[string]any)["properties"].(map[string]any)["id"].(map[string]any)
		if got := id["enum"].([]any); len(got) != 2 || got[0] != float64(10) || got[1] != float64(11) {
			t.Fatalf("id enum = %#v", id["enum"])
		}
		if calls == 1 {
			return sseResponse(`{"translations":[{"id":10,"text":"Olá"},{"id":12,"text":"extra"}]}`), nil
		}
		messages := body["messages"].([]any)
		if len(messages) != 4 || !strings.Contains(messages[3].(map[string]any)["content"].(string), "failed validation") {
			t.Fatalf("correction messages = %#v", messages)
		}
		return sseResponse(`{"translations":[{"id":10,"text":"Olá"},{"id":11,"text":"Mundo"}]}`), nil
	})}
	c := Client{
		APIKey: "x", Model: "x", HTTP: httpClient, Retries: 2,
		Wait: func(context.Context, time.Duration) error { return nil },
		OnRetry: func(_ int, _ time.Duration, reason string) {
			retries++
			retryReason = reason
		},
	}
	got, err := c.Translate(context.Background(), []subtitle.Cue{{Index: 10, Text: "Hello"}, {Index: 11, Text: "World"}}, "en", "pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || retries != 1 || got[0] != "Olá" || got[1] != "Mundo" {
		t.Fatalf("calls=%d retries=%d reason=%q got=%q", calls, retries, retryReason, got)
	}
	if !strings.Contains(retryReason, "inventou a legenda 12") {
		t.Fatalf("retry reason = %q", retryReason)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func sseResponse(parts ...string) *http.Response {
	var body strings.Builder
	for _, part := range parts {
		encoded, _ := json.Marshal(part)
		body.WriteString(`data: {"choices":[{"delta":{"content":`)
		body.Write(encoded)
		body.WriteString("}}]}\n\n")
	}
	body.WriteString("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":8,\"total_tokens\":28,\"cost\":0.000123}}\n\n")
	body.WriteString("data: [DONE]\n\n")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body.String())),
	}
}

type errorBody struct{}

func (errorBody) Read([]byte) (int, error) { return 0, errors.New("simulated read timeout") }
func (errorBody) Close() error             { return nil }
