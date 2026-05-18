package handlers

import (
	"strings"
	"testing"

	"be-miawai/internal/ai"
	"be-miawai/internal/models"
)

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
	if model != "gpt-4o-mini" {
		t.Fatalf("model = %q, want forced gpt-4o-mini", model)
	}
	if string(body) == "" || !containsAll(string(body), `"model":"gpt-4o-mini"`, `"include_usage":true`) {
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

func TestParseOpenAIUsageFromJSON(t *testing.T) {
	usage, ok := parseOpenAIUsageFromJSON([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":7,"total_tokens":17}}`))
	if !ok {
		t.Fatal("parseOpenAIUsageFromJSON() ok = false, want true")
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 7 || usage.TotalTokens != 17 {
		t.Fatalf("usage = %+v, want prompt=10 completion=7 total=17", usage)
	}
}

func TestParseOpenAIUsageFromSSELine(t *testing.T) {
	var usage ai.TokenUsage
	parseOpenAIUsageFromSSELine([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n"), &usage)
	if usage.PromptTokens != 3 || usage.CompletionTokens != 4 || usage.TotalTokens != 7 {
		t.Fatalf("usage = %+v, want prompt=3 completion=4 total=7", usage)
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
