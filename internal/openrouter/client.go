package openrouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/antonioneris/subgen/internal/subtitle"
)

const (
	ProviderOpenRouter = "openrouter"
	ProviderDeepSeek   = "deepseek"
	DefaultEndpoint    = "https://openrouter.ai/api/v1/chat/completions"
	DeepSeekEndpoint   = "https://api.deepseek.com/chat/completions"
)

type Client struct {
	APIKey          string
	Model           string
	ProviderName    string
	Endpoint        string
	PricingEndpoint string
	HTTP            *http.Client
	Retries         int
	AppTitle        string
	Timeout         time.Duration
	OnRetry         func(attempt int, delay time.Duration, reason string)
	OnProgress      func(StreamProgress)
	Wait            func(context.Context, time.Duration) error
}

type ModelPricing struct {
	ModelID            string
	PromptPerToken     float64
	CompletionPerToken float64
	PerRequest         float64
}

func (c *Client) FetchModelPricing(ctx context.Context) (ModelPricing, error) {
	endpoint := c.PricingEndpoint
	if endpoint == "" {
		model := strings.TrimPrefix(c.Model, "/")
		endpoint = "https://openrouter.ai/api/v1/model/" + model
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ModelPricing{}, err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return ModelPricing{}, fmt.Errorf("consultar preço do OpenRouter: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return ModelPricing{}, apiStatusError("OpenRouter", resp.StatusCode, data)
	}
	var payload struct {
		Data struct {
			ID      string `json:"id"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
				Request    string `json:"request"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&payload); err != nil {
		return ModelPricing{}, fmt.Errorf("ler preço do OpenRouter: %w", err)
	}
	prompt, err := strconv.ParseFloat(payload.Data.Pricing.Prompt, 64)
	if err != nil {
		return ModelPricing{}, fmt.Errorf("preço de entrada inválido: %w", err)
	}
	completion, err := strconv.ParseFloat(payload.Data.Pricing.Completion, 64)
	if err != nil {
		return ModelPricing{}, fmt.Errorf("preço de saída inválido: %w", err)
	}
	requestCost := 0.0
	if payload.Data.Pricing.Request != "" {
		requestCost, err = strconv.ParseFloat(payload.Data.Pricing.Request, 64)
		if err != nil {
			return ModelPricing{}, fmt.Errorf("preço por chamada inválido: %w", err)
		}
	}
	return ModelPricing{ModelID: payload.Data.ID, PromptPerToken: prompt, CompletionPerToken: completion, PerRequest: requestCost}, nil
}

type StreamProgress struct {
	ReceivedBytes  int
	CompletedItems int
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CostUSD          float64
	Requests         int
	CostedRequests   int
}

func (u *Usage) Add(other Usage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
	u.CostUSD += other.CostUSD
	u.Requests += other.Requests
	u.CostedRequests += other.CostedRequests
}

func (u Usage) CostKnown() bool {
	return u.Requests > 0 && u.CostedRequests == u.Requests
}

type TranslationResult struct {
	Texts []string
	Usage Usage
}

type translationItem struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

type translationPayload struct {
	Translations []translationItem `json:"translations"`
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []message      `json:"messages"`
	Temperature    float64        `json:"temperature"`
	ResponseFormat map[string]any `json:"response_format"`
	Provider       map[string]any `json:"provider,omitempty"`
	Reasoning      map[string]any `json:"reasoning,omitempty"`
	Thinking       map[string]any `json:"thinking,omitempty"`
	MaxTokens      int            `json:"max_tokens,omitempty"`
	Stream         bool           `json:"stream,omitempty"`
}

type message struct{ Role, Content string }

func (m message) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{m.Role, m.Content})
}

func (c *Client) Translate(ctx context.Context, cues []subtitle.Cue, source, target string) ([]string, error) {
	return c.TranslateWithContext(ctx, cues, nil, nil, source, target)
}

// TranslateWithContext translates only cues. Neighboring cues are sent as
// read-only context so parallel segments keep names, tone and continuity.
func (c *Client) TranslateWithContext(ctx context.Context, cues, before, after []subtitle.Cue, source, target string) ([]string, error) {
	result, err := c.TranslateWithContextUsage(ctx, cues, before, after, source, target)
	return result.Texts, err
}

func (c *Client) TranslateWithContextUsage(ctx context.Context, cues, before, after []subtitle.Cue, source, target string) (TranslationResult, error) {
	if c.APIKey == "" {
		return TranslationResult{}, fmt.Errorf("chave do %s não configurada", c.providerLabel())
	}
	if c.Model == "" {
		return TranslationResult{}, fmt.Errorf("modelo OpenRouter não informado")
	}
	items := make([]translationItem, len(cues))
	for i, cue := range cues {
		items[i] = translationItem{ID: cue.Index, Text: cue.Text}
	}
	input, err := json.Marshal(map[string]any{
		"source_language": source,
		"target_language": target,
		"context_before":  cueItems(before),
		"subtitles":       items,
		"context_after":   cueItems(after),
	})
	if err != nil {
		return TranslationResult{}, err
	}

	system := `You are a professional audiovisual subtitle translator. Translate natural meaning, tone, humor and character voice, not word by word. Keep each subtitle concise enough for its existing screen time. Preserve line breaks when useful, speaker dashes, music symbols, HTML tags and formatting. context_before and context_after are read-only continuity hints: never return translations for them. Never alter IDs. Return only one valid JSON object in exactly this shape: {"translations":[{"id":1,"text":"translated text"}]}. Include every subtitles ID exactly once. Do not add notes. If source_language is "auto", detect it from context.`
	req := chatRequest{
		Model:       c.Model,
		Messages:    []message{{"system", system}, {"user", string(input)}},
		Temperature: 0.2,
		MaxTokens:   completionBudget(cues),
		Stream:      true,
	}
	if c.provider() == ProviderDeepSeek {
		req.Model = strings.TrimPrefix(req.Model, "deepseek/")
		req.ResponseFormat = map[string]any{"type": "json_object"}
		req.Thinking = map[string]any{"type": "disabled"}
	} else {
		req.ResponseFormat = schema(cues)
		req.Provider = map[string]any{"require_parameters": true, "sort": "throughput", "allow_fallbacks": true}
		req.Reasoning = map[string]any{"enabled": false}
	}
	validationRetries := min(max(c.Retries, 0), 2)
	var totalUsage Usage
	for attempt := 0; ; attempt++ {
		content, usage, err := c.stream(ctx, req)
		totalUsage.Add(usage)
		if err != nil {
			return TranslationResult{Usage: totalUsage}, err
		}
		result, validationErr := validateTranslation(cues, content)
		if validationErr == nil {
			return TranslationResult{Texts: result, Usage: totalUsage}, nil
		}
		if attempt >= validationRetries || ctx.Err() != nil {
			return TranslationResult{Usage: totalUsage}, validationErr
		}
		delay := backoff(attempt)
		c.notifyRetry(attempt+1, delay, "resposta inválida: "+validationErr.Error())
		if err := c.pause(ctx, delay); err != nil {
			return TranslationResult{Usage: totalUsage}, err
		}
		req.Messages = append(req.Messages,
			message{"assistant", content},
			message{"user", fmt.Sprintf("Your response failed validation: %s. Regenerate the complete JSON from scratch. Return exactly the %d requested subtitle IDs, each once, and no other ID.", validationErr, len(cues))},
		)
	}
}

func validateTranslation(cues []subtitle.Cue, content string) ([]string, error) {
	var payload translationPayload
	content = strings.TrimSpace(content)
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil, fmt.Errorf("modelo retornou JSON inválido: %w", err)
	}
	byID := make(map[int]string, len(payload.Translations))
	expected := make(map[int]bool, len(cues))
	for _, cue := range cues {
		expected[cue.Index] = true
	}
	for _, item := range payload.Translations {
		if !expected[item.ID] {
			return nil, fmt.Errorf("modelo inventou a legenda %d", item.ID)
		}
		if _, exists := byID[item.ID]; exists {
			return nil, fmt.Errorf("modelo duplicou a legenda %d", item.ID)
		}
		byID[item.ID] = item.Text
	}
	if len(byID) != len(cues) {
		return nil, fmt.Errorf("modelo retornou %d de %d legendas", len(byID), len(cues))
	}
	result := make([]string, len(cues))
	for i, cue := range cues {
		translated, ok := byID[cue.Index]
		if !ok {
			return nil, fmt.Errorf("modelo omitiu a legenda %d", cue.Index)
		}
		if strings.TrimSpace(cue.Text) != "" && strings.TrimSpace(translated) == "" {
			return nil, fmt.Errorf("modelo esvaziou a legenda %d", cue.Index)
		}
		result[i] = translated
	}
	return result, nil
}

func cueItems(cues []subtitle.Cue) []translationItem {
	items := make([]translationItem, len(cues))
	for i, cue := range cues {
		items[i] = translationItem{ID: cue.Index, Text: cue.Text}
	}
	return items
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
	Usage *struct {
		PromptTokens     int      `json:"prompt_tokens"`
		CompletionTokens int      `json:"completion_tokens"`
		TotalTokens      int      `json:"total_tokens"`
		Cost             *float64 `json:"cost"`
	} `json:"usage,omitempty"`
}

func (c *Client) stream(ctx context.Context, request chatRequest) (string, Usage, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return "", Usage{}, err
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		if c.provider() == ProviderDeepSeek {
			endpoint = DeepSeekEndpoint
		} else {
			endpoint = DefaultEndpoint
		}
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	idleTimeout := c.Timeout
	if idleTimeout <= 0 {
		idleTimeout = 15 * time.Minute
	}
	retries := max(c.Retries, 0)

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return "", Usage{}, err
		}
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if c.AppTitle != "" && c.provider() == ProviderOpenRouter {
			req.Header.Set("X-OpenRouter-Title", c.AppTitle)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			if attempt < retries && ctx.Err() == nil {
				delay := backoff(attempt)
				c.notifyRetry(attempt+1, delay, shortReason(err))
				if err := c.pause(ctx, delay); err != nil {
					return "", Usage{}, err
				}
				continue
			}
			return "", Usage{}, fmt.Errorf("conectar ao %s: %w", c.providerLabel(), err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
			resp.Body.Close()
			if attempt < retries && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) {
				delay := backoff(attempt)
				if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
					delay = time.Duration(seconds) * time.Second
				}
				c.notifyRetry(attempt+1, delay, fmt.Sprintf("HTTP %d", resp.StatusCode))
				if err := c.pause(ctx, delay); err != nil {
					return "", Usage{}, err
				}
				continue
			}
			return "", Usage{}, apiStatusError(c.providerLabel(), resp.StatusCode, data)
		}
		content, usage, readErr := readSSE(ctx, resp.Body, idleTimeout, c.OnProgress)
		resp.Body.Close()
		if readErr == nil {
			return content, usage, nil
		}
		if attempt < retries && ctx.Err() == nil {
			delay := backoff(attempt)
			c.notifyRetry(attempt+1, delay, "stream interrompido/sem atividade")
			if err := c.pause(ctx, delay); err != nil {
				return "", Usage{}, err
			}
			continue
		}
		return "", usage, fmt.Errorf("ler stream do %s: %w", c.providerLabel(), readErr)
	}
}

type sseLine struct {
	text string
	err  error
}

func readSSE(ctx context.Context, body io.ReadCloser, idleTimeout time.Duration, onProgress func(StreamProgress)) (string, Usage, error) {
	lines := make(chan sseLine, 16)
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			select {
			case lines <- sseLine{text: scanner.Text()}:
			case <-done:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case lines <- sseLine{err: err}:
			case <-done:
			}
		}
	}()
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	var content strings.Builder
	var usage Usage
	for {
		select {
		case <-ctx.Done():
			_ = body.Close()
			return "", usage, ctx.Err()
		case <-timer.C:
			_ = body.Close()
			return "", usage, fmt.Errorf("nenhum dado recebido por %s", idleTimeout)
		case line, ok := <-lines:
			if !ok {
				return "", usage, fmt.Errorf("stream encerrado sem marcador final")
			}
			if line.err != nil {
				return "", usage, line.err
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idleTimeout)
			if strings.HasPrefix(line.text, ":") || strings.TrimSpace(line.text) == "" {
				continue
			}
			if !strings.HasPrefix(line.text, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line.text, "data:"))
			if data == "[DONE]" {
				return content.String(), usage, nil
			}
			var chunk streamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return "", usage, fmt.Errorf("evento SSE inválido: %w", err)
			}
			if chunk.Error != nil {
				return "", usage, fmt.Errorf("provedor de IA: %s", chunk.Error.Message)
			}
			if chunk.Usage != nil {
				usage.PromptTokens = chunk.Usage.PromptTokens
				usage.CompletionTokens = chunk.Usage.CompletionTokens
				usage.TotalTokens = chunk.Usage.TotalTokens
				usage.Requests = 1
				if chunk.Usage.Cost != nil {
					usage.CostUSD = *chunk.Usage.Cost
					usage.CostedRequests = 1
				}
			}
			for _, choice := range chunk.Choices {
				content.WriteString(choice.Delta.Content)
			}
			if onProgress != nil {
				onProgress(StreamProgress{ReceivedBytes: content.Len(), CompletedItems: completedTranslationItems(content.String())})
			}
		}
	}
}

// completedTranslationItems counts fully closed objects in the streamed
// translations array while ignoring braces escaped inside subtitle strings.
func completedTranslationItems(content string) int {
	key := strings.Index(content, `"translations"`)
	if key < 0 {
		return 0
	}
	start := strings.IndexByte(content[key:], '[')
	if start < 0 {
		return 0
	}
	start += key + 1
	depth, completed := 0, 0
	inString, escaped := false, false
	for i := start; i < len(content); i++ {
		char := content[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 {
					completed++
				}
			}
		case ']':
			if depth == 0 {
				return completed
			}
		}
	}
	return completed
}

func apiStatusError(provider string, status int, data []byte) error {
	var apiErr struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &apiErr)
	message := strings.TrimSpace(apiErr.Error.Message)
	if message == "" {
		message = strings.TrimSpace(string(data))
	}
	return fmt.Errorf("%s HTTP %d: %s", provider, status, message)
}

func (c *Client) provider() string {
	if strings.EqualFold(strings.TrimSpace(c.ProviderName), ProviderDeepSeek) {
		return ProviderDeepSeek
	}
	return ProviderOpenRouter
}

func (c *Client) providerLabel() string {
	if c.provider() == ProviderDeepSeek {
		return "DeepSeek"
	}
	return "OpenRouter"
}

func completionBudget(cues []subtitle.Cue) int {
	estimate := 2_000
	for _, cue := range cues {
		estimate += len([]byte(cue.Text))/2 + 12
	}
	estimate *= 2
	if estimate < 8_192 {
		return 8_192
	}
	if estimate > 262_144 {
		return 262_144
	}
	return estimate
}

func schema(cues []subtitle.Cue) map[string]any {
	ids := make([]int, len(cues))
	for i, cue := range cues {
		ids[i] = cue.Index
	}
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": "subtitle_translation", "strict": true,
			"schema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"translations": map[string]any{
					"type": "array", "minItems": len(cues), "maxItems": len(cues), "items": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{"id": map[string]any{"type": "integer", "enum": ids}, "text": map[string]any{"type": "string"}},
						"required":   []string{"id", "text"},
					},
				}},
				"required": []string{"translations"},
			},
		},
	}
}

func (c *Client) notifyRetry(attempt int, delay time.Duration, reason string) {
	if c.OnRetry != nil {
		c.OnRetry(attempt, delay, reason)
	}
}

func (c *Client) pause(ctx context.Context, delay time.Duration) error {
	if c.Wait != nil {
		return c.Wait(ctx, delay)
	}
	return wait(ctx, delay)
}

func shortReason(err error) string {
	message := err.Error()
	if strings.Contains(strings.ToLower(message), "timeout") || strings.Contains(strings.ToLower(message), "deadline") {
		return "timeout"
	}
	return "conexão interrompida"
}

func backoff(attempt int) time.Duration { return time.Duration(1<<min(attempt, 5)) * time.Second }
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
