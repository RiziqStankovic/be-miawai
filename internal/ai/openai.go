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
	if strings.TrimSpace(settings.BaseURL) == "" {
		return "", errors.New("chat base URL is not configured")
	}
	if strings.TrimSpace(settings.Model) == "" {
		return "", errors.New("active model is not configured")
	}

	chatMessages := make([]map[string]string, 0, len(messages)+1)
	if prompt := strings.TrimSpace(settings.SystemPrompt); prompt != "" {
		chatMessages = append(chatMessages, map[string]string{
			"role":    "system",
			"content": prompt,
		})
	}
	chatMessages = append(chatMessages, messages...)

	body, err := json.Marshal(map[string]any{
		"model":    settings.Model,
		"messages": chatMessages,
		"stream":   true,
	})
	if err != nil {
		return "", err
	}

	endpoint, err := resolveEndpoint(settings.BaseURL, "/chat/completions")
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	applyHeaders(req, settings.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", errors.New(fallbackBodyMessage(payload, fmt.Sprintf("provider returned %d", resp.StatusCode)))
	}

	reader := bufio.NewReader(resp.Body)
	var full strings.Builder

	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "data: ") {
			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data: "))
			if payload == "[DONE]" {
				return full.String(), nil
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
			}
			if json.Unmarshal([]byte(payload), &chunk) == nil {
				if chunk.Error != nil {
					return "", errors.New(chunk.Error.Message)
				}

				if len(chunk.Choices) > 0 {
					content := chunk.Choices[0].Delta.Content
					if content == "" {
						content = chunk.Choices[0].Message.Content
					}
					if content != "" {
						full.WriteString(content)
						if err := onDelta(content); err != nil {
							return "", err
						}
					}
				}
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}
	}

	return full.String(), nil
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
