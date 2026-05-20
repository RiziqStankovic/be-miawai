package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type RuntimeSettings struct {
	Provider     string
	BaseURL      string
	APIKey       string
	Model        string
	SystemPrompt string
}

type TokenUsage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

type ChatResult struct {
	Content string
	Usage   TokenUsage
}

type Client struct {
	http *http.Client
}

type PingResult struct {
	OK        bool     `json:"ok"`
	LatencyMs int64    `json:"latencyMs"`
	Status    *int     `json:"status"`
	Message   string   `json:"message"`
	Models    []string `json:"models,omitempty"`
}

func NewClient() *Client {
	return &Client{
		http: &http.Client{Timeout: 90 * time.Second},
	}
}

func (c *Client) Ping(ctx context.Context, settings RuntimeSettings) (PingResult, error) {
	start := time.Now()
	endpoint, err := resolveEndpoint(settings.BaseURL, "/models")
	if err != nil {
		return PingResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return PingResult{}, err
	}
	applyHeaders(req, settings.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return PingResult{}, err
	}
	defer resp.Body.Close()

	latency := time.Since(start).Milliseconds()
	status := resp.StatusCode

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return PingResult{
			OK:        false,
			LatencyMs: latency,
			Status:    &status,
			Message:   fallbackBodyMessage(body, "provider rejected the request"),
		}, nil
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return PingResult{}, err
	}

	models := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		if strings.TrimSpace(item.ID) != "" {
			models = append(models, item.ID)
		}
	}

	return PingResult{
		OK:        true,
		LatencyMs: latency,
		Status:    &status,
		Message:   "connection established",
		Models:    models,
	}, nil
}

func (c *Client) StreamChat(
	ctx context.Context,
	settings RuntimeSettings,
	messages []map[string]string,
	onDelta func(string) error,
) (string, error) {
	chatMessages := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		chatMessages = append(chatMessages, map[string]any{
			"role":    message["role"],
			"content": message["content"],
		})
	}
	return c.StreamChatAny(ctx, settings, chatMessages, onDelta)
}

func (c *Client) StreamChatAny(
	ctx context.Context,
	settings RuntimeSettings,
	messages []map[string]any,
	onDelta func(string) error,
) (string, error) {
	result, err := c.StreamChatAnyWithUsage(ctx, settings, messages, onDelta)
	return result.Content, err
}

func (c *Client) StreamChatAnyWithUsage(
	ctx context.Context,
	settings RuntimeSettings,
	messages []map[string]any,
	onDelta func(string) error,
) (ChatResult, error) {
	content, usage, err := c.chatWithFallback(ctx, settings, messages, true, onDelta)
	if err != nil {
		return ChatResult{}, err
	}
	return ChatResult{Content: content, Usage: usage}, nil
}

func (c *Client) Chat(ctx context.Context, settings RuntimeSettings, messages []map[string]string) (string, error) {
	chatMessages := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		chatMessages = append(chatMessages, map[string]any{
			"role":    message["role"],
			"content": message["content"],
		})
	}
	return c.ChatAny(ctx, settings, chatMessages)
}

func (c *Client) ChatAny(ctx context.Context, settings RuntimeSettings, messages []map[string]any) (string, error) {
	content, _, err := c.chatWithFallback(ctx, settings, messages, false, nil)
	return content, err
}

func (c *Client) chatWithFallback(
	ctx context.Context,
	settings RuntimeSettings,
	messages []map[string]any,
	stream bool,
	onDelta func(string) error,
) (string, TokenUsage, error) {
	content, usage, err := c.chatCompletions(ctx, settings, messages, stream, onDelta)
	if err == nil {
		return sanitizeToolWrappedContent(content), usage, nil
	}

	content, usage, respErr := c.responsesAPI(ctx, settings, messages, stream, onDelta)
	if respErr == nil {
		return sanitizeToolWrappedContent(content), usage, nil
	}

	return "", TokenUsage{}, errors.Join(err, respErr)
}

func (c *Client) chatCompletions(
	ctx context.Context,
	settings RuntimeSettings,
	messages []map[string]any,
	stream bool,
	onDelta func(string) error,
) (string, TokenUsage, error) {
	if strings.TrimSpace(settings.BaseURL) == "" {
		return "", TokenUsage{}, errors.New("chat base URL is not configured")
	}
	if strings.TrimSpace(settings.Model) == "" {
		return "", TokenUsage{}, errors.New("active model is not configured")
	}

	chatMessages := make([]map[string]any, 0, len(messages)+1)
	if prompt := strings.TrimSpace(settings.SystemPrompt); prompt != "" {
		chatMessages = append(chatMessages, map[string]any{
			"role":    "system",
			"content": prompt,
		})
	}
	chatMessages = append(chatMessages, messages...)

	payload := map[string]any{
		"model":    settings.Model,
		"messages": chatMessages,
		"stream":   stream,
	}
	if stream {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", TokenUsage{}, err
	}

	endpoint, err := resolveEndpoint(settings.BaseURL, "/chat/completions")
	if err != nil {
		return "", TokenUsage{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", TokenUsage{}, err
	}
	applyHeaders(req, settings.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", TokenUsage{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", TokenUsage{}, errors.New(fallbackBodyMessage(payload, fmt.Sprintf("provider returned %d", resp.StatusCode)))
	}

	if stream {
		reader := bufio.NewReader(resp.Body)
		var full strings.Builder
		var usage TokenUsage

		for {
			line, err := reader.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return "", TokenUsage{}, err
			}

			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "data: ") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data: "))
				if payload == "[DONE]" {
					return full.String(), usage, nil
				}

				var chunk struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
					} `json:"choices"`
					Error *struct {
						Message string `json:"message"`
					} `json:"error"`
					Usage *struct {
						PromptTokens     int `json:"prompt_tokens"`
						CompletionTokens int `json:"completion_tokens"`
						TotalTokens      int `json:"total_tokens"`
					} `json:"usage"`
				}
				if json.Unmarshal([]byte(payload), &chunk) == nil {
					if chunk.Error != nil {
						return "", TokenUsage{}, errors.New(chunk.Error.Message)
					}
					if chunk.Usage != nil {
						usage = TokenUsage{
							PromptTokens:     chunk.Usage.PromptTokens,
							CompletionTokens: chunk.Usage.CompletionTokens,
							TotalTokens:      chunk.Usage.TotalTokens,
						}
					}

					if len(chunk.Choices) > 0 {
						content := chunk.Choices[0].Delta.Content
						if content == "" {
							content = chunk.Choices[0].Message.Content
						}
						if content != "" {
							full.WriteString(content)
							if onDelta != nil {
								if err := onDelta(content); err != nil {
									return "", TokenUsage{}, err
								}
							}
						}
					}
				}
			}

			if errors.Is(err, io.EOF) {
				break
			}
		}

		return full.String(), usage, nil
	}

	var responsePayload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&responsePayload); err != nil {
		return "", TokenUsage{}, err
	}
	if responsePayload.Error != nil {
		return "", TokenUsage{}, errors.New(responsePayload.Error.Message)
	}
	if len(responsePayload.Choices) == 0 {
		return "", TokenUsage{}, errors.New("provider returned no choices")
	}
	usage := TokenUsage{}
	if responsePayload.Usage != nil {
		usage = TokenUsage{
			PromptTokens:     responsePayload.Usage.PromptTokens,
			CompletionTokens: responsePayload.Usage.CompletionTokens,
			TotalTokens:      responsePayload.Usage.TotalTokens,
		}
	}
	return responsePayload.Choices[0].Message.Content, usage, nil
}

func (c *Client) responsesAPI(
	ctx context.Context,
	settings RuntimeSettings,
	messages []map[string]any,
	stream bool,
	onDelta func(string) error,
) (string, TokenUsage, error) {
	if strings.TrimSpace(settings.BaseURL) == "" {
		return "", TokenUsage{}, errors.New("chat base URL is not configured")
	}
	if strings.TrimSpace(settings.Model) == "" {
		return "", TokenUsage{}, errors.New("active model is not configured")
	}

	input := make([]map[string]any, 0, len(messages)+1)
	if prompt := strings.TrimSpace(settings.SystemPrompt); prompt != "" {
		input = append(input, map[string]any{
			"role":    "developer",
			"content": prompt,
		})
	}
	input = append(input, messages...)

	payload := map[string]any{
		"model": settings.Model,
		"input": input,
	}
	if stream {
		payload["stream"] = true
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", TokenUsage{}, err
	}

	endpoint, err := resolveEndpoint(settings.BaseURL, "/responses")
	if err != nil {
		return "", TokenUsage{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", TokenUsage{}, err
	}
	applyHeaders(req, settings.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", TokenUsage{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", TokenUsage{}, errors.New(fallbackBodyMessage(payload, fmt.Sprintf("provider returned %d", resp.StatusCode)))
	}

	if stream {
		reader := bufio.NewReader(resp.Body)
		var full strings.Builder
		var usage TokenUsage

		for {
			line, err := reader.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return "", TokenUsage{}, err
			}

			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "data: ") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data: "))
				if payload == "[DONE]" {
					return full.String(), usage, nil
				}

				var event struct {
					Type       string `json:"type"`
					Delta      string `json:"delta"`
					OutputText string `json:"output_text"`
					Error      *struct {
						Message string `json:"message"`
					} `json:"error"`
					Response *struct {
						Status string `json:"status"`
						Usage  *struct {
							InputTokens  int `json:"input_tokens"`
							OutputTokens int `json:"output_tokens"`
							TotalTokens  int `json:"total_tokens"`
						} `json:"usage"`
					} `json:"response"`
					Usage *struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
						TotalTokens  int `json:"total_tokens"`
					} `json:"usage"`
				}
				if json.Unmarshal([]byte(payload), &event) == nil {
					if event.Error != nil {
						return "", TokenUsage{}, errors.New(event.Error.Message)
					}

					chunk := ""
					if strings.Contains(event.Type, "output_text.delta") {
						chunk = event.Delta
					} else if strings.Contains(event.Type, "output_text") {
						chunk = event.OutputText
					}
					if chunk != "" {
						full.WriteString(chunk)
						if onDelta != nil {
							if err := onDelta(chunk); err != nil {
								return "", TokenUsage{}, err
							}
						}
					}

					if event.Usage != nil {
						usage = TokenUsage{
							PromptTokens:     event.Usage.InputTokens,
							CompletionTokens: event.Usage.OutputTokens,
							TotalTokens:      event.Usage.TotalTokens,
						}
					}
					if event.Response != nil && event.Response.Usage != nil {
						usage = TokenUsage{
							PromptTokens:     event.Response.Usage.InputTokens,
							CompletionTokens: event.Response.Usage.OutputTokens,
							TotalTokens:      event.Response.Usage.TotalTokens,
						}
					}
				}
			}

			if errors.Is(err, io.EOF) {
				break
			}
		}
		return full.String(), usage, nil
	}

	var responsePayload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&responsePayload); err != nil {
		return "", TokenUsage{}, err
	}

	if rawErr, ok := responsePayload["error"]; ok && rawErr != nil {
		if errMap, ok := rawErr.(map[string]any); ok {
			if msg, ok := errMap["message"].(string); ok && strings.TrimSpace(msg) != "" {
				return "", TokenUsage{}, errors.New(msg)
			}
		}
	}

	content := extractResponseText(responsePayload)
	if strings.TrimSpace(content) == "" {
		return "", TokenUsage{}, errors.New("provider returned no text output")
	}

	usage := TokenUsage{}
	if rawUsage, ok := responsePayload["usage"].(map[string]any); ok {
		usage = TokenUsage{
			PromptTokens:     intFromAny(rawUsage["input_tokens"]),
			CompletionTokens: intFromAny(rawUsage["output_tokens"]),
			TotalTokens:      intFromAny(rawUsage["total_tokens"]),
		}
	}

	return content, usage, nil
}

func extractResponseText(payload map[string]any) string {
	if text, ok := payload["text"].(string); ok && strings.TrimSpace(text) != "" {
		return text
	}

	output, ok := payload["output"].([]any)
	if !ok {
		return ""
	}

	var full strings.Builder
	for _, item := range output {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		contentList, ok := obj["content"].([]any)
		if !ok {
			continue
		}
		for _, contentItem := range contentList {
			contentMap, ok := contentItem.(map[string]any)
			if !ok {
				continue
			}
			if contentType, _ := contentMap["type"].(string); contentType == "output_text" {
				if text, ok := contentMap["text"].(string); ok && text != "" {
					full.WriteString(text)
				}
			}
		}
	}

	return full.String()
}

func intFromAny(v any) int {
	switch value := v.(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	default:
		return 0
	}
}

func sanitizeToolWrappedContent(content string) string {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "<attempt_completion>") && strings.Contains(trimmed, "</attempt_completion>") {
		resultStart := strings.Index(trimmed, "<result>")
		resultEnd := strings.Index(trimmed, "</result>")
		if resultStart >= 0 && resultEnd > resultStart {
			start := resultStart + len("<result>")
			return strings.TrimSpace(trimmed[start:resultEnd])
		}
	}
	return content
}

func resolveEndpoint(baseURL string, suffix string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", errors.New("base URL is required")
	}
	return base + suffix, nil
}

func applyHeaders(req *http.Request, apiKey string) {
	req.Header.Set("Accept", "application/json")
	if trimmed := strings.TrimSpace(apiKey); trimmed != "" {
		req.Header.Set("Authorization", "Bearer "+trimmed)
	}
}

func fallbackBodyMessage(body []byte, fallback string) string {
	if len(body) == 0 {
		return fallback
	}

	var payload struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error != nil {
		switch value := payload.Error.(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return value
			}
		case map[string]any:
			if message, ok := value["message"].(string); ok && strings.TrimSpace(message) != "" {
				return message
			}
		}
	}

	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		return trimmed
	}
	return fallback
}