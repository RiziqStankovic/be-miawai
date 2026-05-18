package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"be-miawai/internal/ai"
	"be-miawai/internal/database"
	"be-miawai/internal/models"
)

type apiKeyCreateResponse struct {
	Key      models.UserAPIKey `json:"key"`
	PlainKey string            `json:"plainKey"`
}

type apiGatewayIdentity struct {
	Key  models.UserAPIKey
	User models.User
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request, user models.User) {
	keys, err := s.store.ListUserAPIKeys(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load api keys")
		return
	}
	writeJSON(w, http.StatusOK, map[string][]models.UserAPIKey{"keys": keys})
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	credential, err := s.store.CreateUserAPIKey(r.Context(), user.ID, body.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create api key")
		return
	}
	writeJSON(w, http.StatusCreated, apiKeyCreateResponse{
		Key:      credential.Key,
		PlainKey: credential.PlainKey,
	})
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request, user models.User) {
	keyID := strings.TrimSpace(r.PathValue("id"))
	if keyID == "" {
		writeError(w, http.StatusBadRequest, "api key id is required")
		return
	}
	if err := s.store.RevokeUserAPIKey(r.Context(), user.ID, keyID); err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "api key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to revoke api key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) openAIModelsGateway(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticateGatewayRequest(w, r)
	if !ok {
		return
	}
	settings, ok := s.gatewayRuntimeSettings(w, identity.User)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeGatewayModelsResponse(w, settings)
		log.Printf("api gateway models local key_prefix=%s user=%s method=%s", identity.Key.KeyPrefix, identity.User.ID, r.Method)
		return
	}
	s.proxySimpleGatewayRequest(w, r, identity, settings, "/models")
}

func (s *Server) openAIChatCompletionsGateway(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticateGatewayRequest(w, r)
	if !ok {
		return
	}
	settings, ok := s.gatewayRuntimeSettings(w, identity.User)
	if !ok {
		return
	}
	hasCredit, err := s.hasRemainingWeeklyUsageCredit(r.Context(), identity.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check usage credit")
		return
	}
	if !hasCredit {
		writeError(w, http.StatusPaymentRequired, "usage credit is exhausted")
		return
	}

	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	body, stream, model, err := normalizeGatewayChatBody(rawBody, identity.User)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if stream {
		s.proxyStreamingChatGateway(w, r, identity, settings, body, model)
		return
	}
	s.proxyJSONChatGateway(w, r, identity, settings, body, model)
}

func (s *Server) openAIUnsupportedGateway(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		if _, ok := s.authenticateGatewayRequest(w, r); !ok {
			return
		}
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error": map[string]any{
				"message": "this OpenAI-compatible endpoint is not supported by Miaw gateway yet",
				"type":    "unsupported_endpoint",
			},
		})
		return
	}
	http.NotFound(w, r)
}

func (s *Server) authenticateGatewayRequest(w http.ResponseWriter, r *http.Request) (apiGatewayIdentity, bool) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(raw), "bearer ") {
		writeOpenAIError(w, http.StatusUnauthorized, "missing bearer api key", "invalid_request_error")
		return apiGatewayIdentity{}, false
	}
	plainKey := strings.TrimSpace(raw[len("Bearer "):])
	key, user, err := s.store.AuthenticateUserAPIKey(r.Context(), plainKey)
	if errors.Is(err, database.ErrAPIKeyInvalid) {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid api key", "invalid_api_key")
		return apiGatewayIdentity{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authenticate api key")
		return apiGatewayIdentity{}, false
	}
	user = decorateUserAccess(user)
	if err := s.store.MarkUserAPIKeyUsed(r.Context(), key.ID); err != nil {
		log.Printf("api gateway mark used failed key_prefix=%s user=%s error=%q", key.KeyPrefix, user.ID, err.Error())
	}
	return apiGatewayIdentity{Key: key, User: user}, true
}

func (s *Server) gatewayRuntimeSettings(w http.ResponseWriter, user models.User) (models.RuntimeSettings, bool) {
	settings := s.defaultRuntimeSettings()
	settings.APIKey = strings.TrimSpace(firstNonEmpty(s.cfg.ManagedAIApiKey, s.cfg.DefaultProviderAPIKey))
	if strings.TrimSpace(settings.BaseURL) == "" || strings.TrimSpace(settings.APIKey) == "" {
		writeError(w, http.StatusBadGateway, "managed ai provider is not configured")
		return models.RuntimeSettings{}, false
	}
	if user.Plan != "pro" {
		settings.Models.Active = "gpt-4o-mini"
	}
	return settings, true
}

func (s *Server) proxySimpleGatewayRequest(w http.ResponseWriter, r *http.Request, identity apiGatewayIdentity, settings models.RuntimeSettings, suffix string) {
	endpoint := strings.TrimRight(settings.BaseURL, "/") + suffix
	req, err := http.NewRequestWithContext(r.Context(), r.Method, endpoint, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to build upstream request")
		return
	}
	copyGatewayHeaders(req.Header, r.Header, settings.APIKey)
	resp, err := s.client.Do(req)
	if err != nil {
		if suffix == "/models" {
			writeGatewayModelsResponse(w, settings)
			log.Printf("api gateway models local_fallback key_prefix=%s user=%s error=%q", identity.Key.KeyPrefix, identity.User.ID, err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "upstream request failed")
		return
	}
	defer resp.Body.Close()
	if suffix == "/models" && resp.StatusCode >= 400 {
		writeGatewayModelsResponse(w, settings)
		log.Printf("api gateway models local_fallback key_prefix=%s user=%s status=%d", identity.Key.KeyPrefix, identity.User.ID, resp.StatusCode)
		return
	}
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	log.Printf("api gateway models key_prefix=%s user=%s status=%d", identity.Key.KeyPrefix, identity.User.ID, resp.StatusCode)
}

func writeGatewayModelsResponse(w http.ResponseWriter, settings models.RuntimeSettings) {
	models := gatewayModelIDs(settings)
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		data = append(data, map[string]any{
			"id":       model,
			"object":   "model",
			"owned_by": "miaw",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

func gatewayModelIDs(settings models.RuntimeSettings) []string {
	seen := map[string]struct{}{}
	models := make([]string, 0, len(settings.Models.All)+2)
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		if _, ok := seen[model]; ok {
			return
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	add(settings.Models.Active)
	for _, model := range settings.Models.All {
		add(model)
	}
	add("customai-tunning")
	if len(models) == 0 {
		add("gpt-4o-mini")
	}
	return models
}

func (s *Server) proxyJSONChatGateway(w http.ResponseWriter, r *http.Request, identity apiGatewayIdentity, settings models.RuntimeSettings, body []byte, model string) {
	respBody, status, headers, err := s.doGatewayChatRequest(r.Context(), r.Header, settings, body)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if status >= 300 {
		writeGatewayRawResponse(w, status, headers, respBody)
		log.Printf("api gateway chat upstream_error key_prefix=%s user=%s model=%s status=%d body=%s", identity.Key.KeyPrefix, identity.User.ID, model, status, logValue(string(respBody), 240))
		return
	}
	usage, ok := parseOpenAIUsageFromJSON(respBody)
	if !ok {
		writeOpenAIError(w, http.StatusNotImplemented, "upstream response did not include billable usage", "unsupported_unmetered_response")
		log.Printf("api gateway chat missing_usage key_prefix=%s user=%s model=%s status=%d", identity.Key.KeyPrefix, identity.User.ID, model, status)
		return
	}
	if err := s.recordGatewayUsage(r.Context(), identity, usage, model, status); err != nil {
		log.Printf("api gateway usage tracking failed key_prefix=%s user=%s model=%s error=%q", identity.Key.KeyPrefix, identity.User.ID, model, err.Error())
	}
	writeGatewayRawResponse(w, status, headers, respBody)
}

func (s *Server) proxyStreamingChatGateway(w http.ResponseWriter, r *http.Request, identity apiGatewayIdentity, settings models.RuntimeSettings, body []byte, model string) {
	endpoint := strings.TrimRight(settings.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to build upstream request")
		return
	}
	copyGatewayHeaders(req.Header, r.Header, settings.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream request failed")
		return
	}
	defer resp.Body.Close()
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_, _ = w.Write(payload)
		if flusher != nil {
			flusher.Flush()
		}
		log.Printf("api gateway stream upstream_error key_prefix=%s user=%s model=%s status=%d body=%s", identity.Key.KeyPrefix, identity.User.ID, model, resp.StatusCode, logValue(string(payload), 240))
		return
	}

	reader := bufio.NewReader(resp.Body)
	var usage ai.TokenUsage
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			parseOpenAIUsageFromSSELine(line, &usage)
			_, _ = w.Write(line)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				log.Printf("api gateway stream read failed key_prefix=%s user=%s model=%s error=%q", identity.Key.KeyPrefix, identity.User.ID, model, readErr.Error())
			}
			break
		}
	}
	if usage.TotalTokens == 0 && usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		log.Printf("api gateway stream missing_usage key_prefix=%s user=%s model=%s status=%d", identity.Key.KeyPrefix, identity.User.ID, model, resp.StatusCode)
		return
	}
	if err := s.recordGatewayUsage(context.Background(), identity, usage, model, resp.StatusCode); err != nil {
		log.Printf("api gateway stream usage tracking failed key_prefix=%s user=%s model=%s error=%q", identity.Key.KeyPrefix, identity.User.ID, model, err.Error())
	}
}

func (s *Server) doGatewayChatRequest(ctx context.Context, headers http.Header, settings models.RuntimeSettings, body []byte) ([]byte, int, http.Header, error) {
	endpoint := strings.TrimRight(settings.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, nil, err
	}
	copyGatewayHeaders(req.Header, headers, settings.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, nil, err
	}
	return payload, resp.StatusCode, resp.Header.Clone(), nil
}

func (s *Server) recordGatewayUsage(ctx context.Context, identity apiGatewayIdentity, usage ai.TokenUsage, model string, status int) error {
	if usage.TotalTokens > 0 && usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		usage.PromptTokens = usage.TotalTokens
	}
	err := s.store.IncrementDailyUsage(ctx, identity.User.ID, usage.PromptTokens, usage.CompletionTokens, 0, 0)
	log.Printf("api gateway chat key_prefix=%s user=%s model=%s status=%d input_tokens=%d output_tokens=%d total_tokens=%d", identity.Key.KeyPrefix, identity.User.ID, model, status, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	return err
}

func normalizeGatewayChatBody(rawBody []byte, user models.User) ([]byte, bool, string, error) {
	var payload map[string]any
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		return nil, false, "", errors.New("invalid json body")
	}
	if _, ok := payload["messages"]; !ok {
		return nil, false, "", errors.New("messages is required")
	}
	model, _ := payload["model"].(string)
	model = strings.TrimSpace(model)
	if user.Plan != "pro" {
		model = "gpt-4o-mini"
		payload["model"] = model
	}
	stream, _ := payload["stream"].(bool)
	if stream {
		options, _ := payload["stream_options"].(map[string]any)
		if options == nil {
			options = map[string]any{}
		}
		options["include_usage"] = true
		payload["stream_options"] = options
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, false, "", err
	}
	return body, stream, model, nil
}

func parseOpenAIUsageFromJSON(body []byte) (ai.TokenUsage, bool) {
	var payload struct {
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Usage == nil {
		return ai.TokenUsage{}, false
	}
	return ai.TokenUsage{
		PromptTokens:     payload.Usage.PromptTokens,
		CompletionTokens: payload.Usage.CompletionTokens,
		TotalTokens:      payload.Usage.TotalTokens,
	}, true
}

func parseOpenAIUsageFromSSELine(line []byte, usage *ai.TokenUsage) {
	trimmed := strings.TrimSpace(string(line))
	if !strings.HasPrefix(trimmed, "data: ") {
		return
	}
	data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data: "))
	if data == "" || data == "[DONE]" {
		return
	}
	if parsed, ok := parseOpenAIUsageFromJSON([]byte(data)); ok {
		*usage = parsed
	}
}

func copyGatewayHeaders(dst http.Header, src http.Header, apiKey string) {
	for name, values := range src {
		lower := strings.ToLower(name)
		switch lower {
		case "authorization", "host", "content-length", "connection":
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
	dst.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	dst.Set("Accept", "application/json")
}

func copyResponseHeaders(dst http.Header, src http.Header) {
	for name, values := range src {
		lower := strings.ToLower(name)
		if lower == "content-length" || lower == "connection" {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func writeGatewayRawResponse(w http.ResponseWriter, status int, headers http.Header, body []byte) {
	copyResponseHeaders(w.Header(), headers)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeOpenAIError(w http.ResponseWriter, status int, message string, errorType string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    firstNonEmpty(errorType, "invalid_request_error"),
		},
	})
}
