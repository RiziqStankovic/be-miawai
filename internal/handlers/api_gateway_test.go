package handlers

import (
	"strings"
	"testing"
	"time"

	"be-miawai/internal/ai"
	"be-miawai/internal/config"
	"be-miawai/internal/models"
)

func TestNewServerUsesNoTotalTimeoutForGatewayClient(t *testing.T) {
	server := NewServer(config.Config{}, nil)

	if server.gatewayClient == nil {
		t.Fatal("gatewayClient is nil")
	}
	if server.gatewayClient.Timeout != 0 {
		t.Fatalf("gatewayClient timeout = %s, want no total timeout", server.gatewayClient.Timeout)
	}
	if server.client.Timeout != 12*time.Second {
		t.Fatalf("default client timeout = %s, want 12s", server.client.Timeout)
	}
}

func TestNormalizeGatewayChatBodyForFreeUserForcesModelAndUsage(t *testing.T) {
	body, stream, model, err := normalizeGatewayChatBody(
		[]byte(`{"model":"gpt-4.1","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		models.User{Plan: "free"},
	)
	if err != nil {
		t.Fatalf("normalizeGatewayChatBody() error = %v", err)
	}
	if !stream {
		t.Fatal("stream = false, want true")
	}
	if model != managedFreeTierModel {
		t.Fatalf("model = %q, want forced %s", model, managedFreeTierModel)
	}
	if string(body) == "" || !containsAll(string(body), `"model":"`+managedFreeTierModel+`"`, `"include_usage":true`) {
		t.Fatalf("normalized body missing forced model/include_usage: %s", string(body))
	}
}

func TestNormalizeGatewayChatBodyForProUserKeepsModel(t *testing.T) {
	_, _, model, err := normalizeGatewayChatBody(
		[]byte(`{"model":"gpt-4.1","messages":[{"role":"user","content":"hi"}]}`),
		models.User{Plan: "pro"},
	)
	if err != nil {
		t.Fatalf("normalizeGatewayChatBody() error = %v", err)
	}
	if model != "gpt-4.1" {
		t.Fatalf("model = %q, want original model", model)
	}
}

func TestNormalizeGatewayResponsesBodyForFreeUserForcesModelWithoutMessages(t *testing.T) {
	body, stream, model, err := normalizeGatewayResponsesBody(
		[]byte(`{"model":"gpt-5.4-mini","input":[{"type":"message","role":"user","content":"hi"}],"stream":true}`),
		models.User{Plan: "free"},
	)
	if err != nil {
		t.Fatalf("normalizeGatewayResponsesBody() error = %v", err)
	}
	if !stream {
		t.Fatal("stream = false, want true")
	}
	if model != managedFreeTierModel {
		t.Fatalf("model = %q, want forced %s", model, managedFreeTierModel)
	}
	got := string(body)
	if !containsAll(got, `"model":"`+managedFreeTierModel+`"`, `"input":[`) {
		t.Fatalf("normalized body missing forced model/input: %s", got)
	}
	if strings.Contains(got, `"messages"`) || strings.Contains(got, `"instructions"`) {
		t.Fatalf("normalized responses body unexpectedly added chat fields: %s", got)
	}
}

func TestNormalizeGatewayResponsesBodyRequiresInput(t *testing.T) {
	_, _, _, err := normalizeGatewayResponsesBody(
		[]byte(`{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"hi"}]}`),
		models.User{Plan: "pro"},
	)
	if err == nil || !strings.Contains(err.Error(), "input is required") {
		t.Fatalf("error = %v, want input is required", err)
	}
}

func TestParseOpenAIUsageFromJSON(t *testing.T) {
	usage, ok := parseOpenAIUsageFromJSON([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":7,"total_tokens":17}}`))
	if !ok {
		t.Fatal("parseOpenAIUsageFromJSON() ok = false, want true")
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 7 || usage.TotalTokens != 17 {
		t.Fatalf("usage = %+v, want prompt=10 completion=7 total=17", usage)
	}
}

func TestParseResponsesUsageFromJSON(t *testing.T) {
	usage, ok := parseResponsesUsageFromJSON([]byte(`{"usage":{"input_tokens":11,"output_tokens":6,"total_tokens":17}}`))
	if !ok {
		t.Fatal("parseResponsesUsageFromJSON() ok = false, want true")
	}
	if usage.PromptTokens != 11 || usage.CompletionTokens != 6 || usage.TotalTokens != 17 {
		t.Fatalf("usage = %+v, want prompt=11 completion=6 total=17", usage)
	}
}

func TestParseOpenAIUsageFromSSELine(t *testing.T) {
	var usage ai.TokenUsage
	parseOpenAIUsageFromSSELine([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n"), &usage)
	if usage.PromptTokens != 3 || usage.CompletionTokens != 4 || usage.TotalTokens != 7 {
		t.Fatalf("usage = %+v, want prompt=3 completion=4 total=7", usage)
	}
}

func TestParseResponsesUsageFromSSELine(t *testing.T) {
	var usage ai.TokenUsage
	parseResponsesUsageFromSSELine([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":4,\"total_tokens\":7}}}\n"), &usage)
	if usage.PromptTokens != 3 || usage.CompletionTokens != 4 || usage.TotalTokens != 7 {
		t.Fatalf("usage = %+v, want prompt=3 completion=4 total=7", usage)
	}
}

func TestIsUpstreamBudgetExceeded(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "plain text budget",
			body: "Budget has been exceeded! Current cost: 50.04766749999994, Max budget: 50.0",
			want: true,
		},
		{
			name: "json error message",
			body: `{"error":{"message":"Budget has been exceeded! Current cost: 50.04, Max budget: 50.0"}}`,
			want: true,
		},
		{
			name: "openai quota code",
			body: `{"error":{"message":"You exceeded your current quota","code":"insufficient_quota"}}`,
			want: true,
		},
		{
			name: "ordinary upstream error",
			body: `{"error":{"message":"model is not available"}}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUpstreamBudgetExceeded([]byte(tt.body)); got != tt.want {
				t.Fatalf("isUpstreamBudgetExceeded() = %v, want %v", got, tt.want)
			}
		})
	}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
