package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"be-miawai/internal/ai"
	"be-miawai/internal/auth"
	"be-miawai/internal/config"
	"be-miawai/internal/database"
	"be-miawai/internal/models"
)

type Server struct {
	cfg      config.Config
	store    *database.Store
	sessions *auth.Manager
	guests   *auth.GuestManager
	client   *http.Client
	aiClient *ai.Client
}

type errorResponse struct {
	Error string `json:"error"`
}

func NewServer(cfg config.Config, store *database.Store) *Server {
	return &Server{
		cfg:      cfg,
		store:    store,
		sessions: auth.NewManager(cfg.SessionSecret, cfg.CookieSecure),
		guests:   auth.NewGuestManager(cfg.SessionSecret, cfg.CookieSecure),
		client:   &http.Client{Timeout: 12 * time.Second},
		aiClient: ai.NewClient(),
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/app/bootstrap", s.requireUser(s.bootstrap))
	mux.HandleFunc("GET /v1/guest/session", s.guestSession)
	mux.HandleFunc("POST /v1/guest/chat/stream", s.guestChatStream)
	mux.HandleFunc("POST /v1/auth/dev-login", s.devLogin)
	mux.HandleFunc("GET /v1/auth/google/login", s.oauthLogin("google"))
	mux.HandleFunc("GET /v1/auth/github/login", s.oauthLogin("github"))
	mux.HandleFunc("GET /v1/auth/google/callback", s.oauthCallback("google"))
	mux.HandleFunc("GET /v1/auth/github/callback", s.oauthCallback("github"))
	mux.HandleFunc("POST /v1/auth/logout", s.logout)
	mux.HandleFunc("GET /v1/me", s.requireUser(s.me))
	mux.HandleFunc("GET /v1/runtime/settings", s.requireUser(s.runtimeSettings))
	mux.HandleFunc("PUT /v1/runtime/settings", s.requireUser(s.updateRuntimeSettings))
	mux.HandleFunc("POST /v1/runtime/settings/test", s.requireUser(s.testRuntimeSettings))
	mux.HandleFunc("GET /v1/conversations", s.requireUser(s.listConversations))
	mux.HandleFunc("GET /v1/conversations/{id}", s.requireUser(s.getConversation))
	mux.HandleFunc("PATCH /v1/conversations/{id}", s.requireUser(s.updateConversation))
	mux.HandleFunc("DELETE /v1/conversations/{id}", s.requireUser(s.deleteConversation))
	mux.HandleFunc("GET /v1/chat/history", s.requireUser(s.chatHistory))
	mux.HandleFunc("POST /v1/chat", s.requireUser(s.chat))
	mux.HandleFunc("POST /v1/chat/stream", s.requireUser(s.chatStream))
	mux.HandleFunc("GET /v1/subscriptions/entitlement", s.requireUser(s.me))
	mux.HandleFunc("POST /v1/subscriptions/platform", s.requireUser(s.platformSubscription))
	mux.HandleFunc("POST /v1/webhooks/subscriptions", s.subscriptionWebhook)
	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) guestSession(w http.ResponseWriter, r *http.Request) {
	used := s.readGuestUsage(r)
	writeJSON(w, http.StatusOK, guestQuotaPayload(used))
}

func (s *Server) devLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.EnableDevAuth {
		writeError(w, http.StatusForbidden, "dev auth is disabled")
		return
	}

	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	account, ok := lookupDevAccount(body.Email)
	if !ok || account.Password != body.Password {
		writeError(w, http.StatusUnauthorized, "invalid dev credentials")
		return
	}

	user, err := s.store.FindOrCreateOAuthUser(r.Context(), models.OAuthProfile{
		Provider:       "dev",
		ProviderUserID: account.Email,
		Email:          account.Email,
		Name:           account.Name,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create dev session")
		return
	}

	user = decorateUserAccess(user)

	if err := s.sessions.SetSession(w, auth.SessionIdentity{
		UserID: user.ID,
		Email:  user.Email,
		Role:   user.Role,
		Access: user.Access,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	writeJSON(w, http.StatusOK, map[string]models.User{"user": user})
}

func (s *Server) oauthLogin(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state, err := auth.SignOAuthState(s.cfg.SessionSecret, provider)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create oauth state")
			return
		}

		authURL, err := s.buildAuthURL(provider, state)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		http.Redirect(w, r, authURL, http.StatusFound)
	}
}

func (s *Server) oauthCallback(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := auth.VerifyOAuthState(s.cfg.SessionSecret, r.URL.Query().Get("state"), provider); err != nil {
			writeError(w, http.StatusBadRequest, "invalid oauth state")
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			writeError(w, http.StatusBadRequest, "missing oauth code")
			return
		}

		profile, err := s.exchangeOAuthCode(r.Context(), provider, code)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}

		user, err := s.store.FindOrCreateOAuthUser(r.Context(), profile)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to upsert user")
			return
		}

		user = decorateUserAccess(user)

		if err := s.sessions.SetSession(w, auth.SessionIdentity{
			UserID: user.ID,
			Email:  user.Email,
			Role:   user.Role,
			Access: user.Access,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create session")
			return
		}

		http.Redirect(w, r, s.cfg.AppBaseURL, http.StatusFound)
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.sessions.ClearSession(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]models.User{"user": user})
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request, user models.User) {
	settings := models.RuntimeSettings{
		Models: models.RuntimeModels{All: []string{}},
	}
	if hasAccess(user, accessRuntimeRead) {
		var err error
		settings, err = s.loadRuntimeSettings(r.Context(), user.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load runtime settings")
			return
		}
	}

	conversations := []models.Conversation{}
	if hasAccess(user, accessConversationsRead) {
		var err error
		conversations, err = s.store.ListConversationsByUser(r.Context(), user.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load conversations")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user":          user,
		"settings":      settings,
		"conversations": conversations,
	})
}

func (s *Server) runtimeSettings(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessRuntimeRead) {
		writeError(w, http.StatusForbidden, "runtime access is not allowed for this role")
		return
	}

	settings, err := s.loadRuntimeSettings(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load runtime settings")
		return
	}

	writeJSON(w, http.StatusOK, map[string]models.RuntimeSettings{"settings": settings})
}

func (s *Server) updateRuntimeSettings(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessRuntimeWrite) {
		writeError(w, http.StatusForbidden, "runtime editing is not allowed for this role")
		return
	}

	var body struct {
		Provider     string   `json:"provider"`
		BaseURL      string   `json:"baseUrl"`
		APIKey       string   `json:"apiKey"`
		SystemPrompt string   `json:"systemPrompt"`
		Models       []string `json:"models"`
		ActiveModel  string   `json:"activeModel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	settings, err := s.store.UpsertRuntimeSettings(r.Context(), user.ID, models.RuntimeSettings{
		Provider:     body.Provider,
		BaseURL:      body.BaseURL,
		APIKey:       body.APIKey,
		SystemPrompt: body.SystemPrompt,
		Models: models.RuntimeModels{
			Active: body.ActiveModel,
			All:    body.Models,
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save runtime settings")
		return
	}

	writeJSON(w, http.StatusOK, map[string]models.RuntimeSettings{"settings": settings})
}

func (s *Server) testRuntimeSettings(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessRuntimeWrite) {
		writeError(w, http.StatusForbidden, "runtime editing is not allowed for this role")
		return
	}

	var body struct {
		Provider    string   `json:"provider"`
		BaseURL     string   `json:"baseUrl"`
		APIKey      string   `json:"apiKey"`
		Models      []string `json:"models"`
		ActiveModel string   `json:"activeModel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	settings := ai.RuntimeSettings{
		Provider: body.Provider,
		BaseURL:  body.BaseURL,
		APIKey:   body.APIKey,
		Model:    firstNonEmpty(strings.TrimSpace(body.ActiveModel), firstModel(body.Models)),
	}

	result, err := s.aiClient.Ping(r.Context(), settings)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	if len(result.Models) > 1 {
		sort.Strings(result.Models)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessConversationsRead) {
		writeError(w, http.StatusForbidden, "conversation history is not allowed for this role")
		return
	}

	conversations, err := s.store.ListConversationsByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load conversations")
		return
	}

	writeJSON(w, http.StatusOK, map[string][]models.Conversation{"conversations": conversations})
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessConversationsRead) || !hasAccess(user, accessChatRead) {
		writeError(w, http.StatusForbidden, "conversation history is not allowed for this role")
		return
	}

	conversationID := r.PathValue("id")
	messages, err := s.store.ListConversationMessages(r.Context(), user.ID, conversationID)
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load conversation")
		return
	}

	conversation, err := s.store.GetConversationByID(r.Context(), user.ID, conversationID)
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load conversation")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"conversation": conversation,
		"messages":     messages,
	})
}

func (s *Server) updateConversation(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessConversationsWrite) {
		writeError(w, http.StatusForbidden, "conversation updates are not allowed for this role")
		return
	}

	conversationID := r.PathValue("id")
	var body struct {
		Title  *string `json:"title"`
		Pinned *bool   `json:"pinned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	conversation, err := s.store.UpdateConversationMeta(r.Context(), user.ID, conversationID, body.Title, body.Pinned)
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update conversation")
		return
	}

	writeJSON(w, http.StatusOK, map[string]models.Conversation{"conversation": conversation})
}

func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessConversationsDelete) {
		writeError(w, http.StatusForbidden, "conversation deletion is not allowed for this role")
		return
	}

	conversationID := r.PathValue("id")
	if err := s.store.DeleteConversation(r.Context(), user.ID, conversationID); err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete conversation")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) chatHistory(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessChatRead) {
		writeError(w, http.StatusForbidden, "chat history is not allowed for this role")
		return
	}

	messages, err := s.store.ListChatMessagesByUser(r.Context(), user.ID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat history")
		return
	}

	writeJSON(w, http.StatusOK, map[string][]models.ChatMessage{"messages": messages})
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessChatWrite) {
		writeError(w, http.StatusForbidden, "chat sending is not allowed for this role")
		return
	}

	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	message := strings.TrimSpace(body.Message)
	if message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	if _, err := s.store.InsertChatMessage(r.Context(), user.ID, "user", message); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save user message")
		return
	}

	reply := "Miaw web backend received: " + message
	assistantMessage, err := s.store.InsertChatMessage(r.Context(), user.ID, "assistant", reply)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save assistant message")
		return
	}

	writeJSON(w, http.StatusOK, map[string]models.ChatMessage{"message": assistantMessage})
}

func (s *Server) chatStream(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessChatWrite) {
		writeError(w, http.StatusForbidden, "chat sending is not allowed for this role")
		return
	}
	if !hasAccess(user, accessConversationsWrite) {
		writeError(w, http.StatusForbidden, "conversation updates are not allowed for this role")
		return
	}

	var body struct {
		ConversationID string `json:"conversationId"`
		Title          string `json:"title"`
		Message        string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	message := strings.TrimSpace(body.Message)
	if message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	settings, err := s.loadRuntimeSettings(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load runtime settings")
		return
	}
	if strings.TrimSpace(settings.BaseURL) == "" || strings.TrimSpace(settings.Models.Active) == "" {
		writeError(w, http.StatusBadRequest, "runtime settings are incomplete")
		return
	}

	conversationID := strings.TrimSpace(body.ConversationID)
	if conversationID == "" {
		conversation, err := s.store.CreateConversation(
			r.Context(),
			user.ID,
			firstNonEmpty(strings.TrimSpace(body.Title), titleFromMessage(message)),
			settings.Provider,
			settings.Models.Active,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create conversation")
			return
		}
		conversationID = conversation.ID
	} else {
		if _, err := s.store.GetConversationByID(r.Context(), user.ID, conversationID); err != nil {
			if database.IsNotFound(err) {
				writeError(w, http.StatusNotFound, "conversation not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to load conversation")
			return
		}
	}

	if _, err := s.store.AddConversationMessage(r.Context(), user.ID, conversationID, "user", message); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save message")
		return
	}

	promptMessages, err := s.store.ConversationPromptMessages(r.Context(), user.ID, conversationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build prompt")
		return
	}

	conversation, err := s.store.GetConversationByID(r.Context(), user.ID, conversationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to refresh conversation")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if err := writeStreamEvent(w, map[string]any{
		"type":         "meta",
		"conversation": conversation,
	}); err != nil {
		return
	}
	flusher.Flush()

	assistantText, err := s.aiClient.StreamChat(
		r.Context(),
		ai.RuntimeSettings{
			Provider:     settings.Provider,
			BaseURL:      settings.BaseURL,
			APIKey:       settings.APIKey,
			Model:        settings.Models.Active,
			SystemPrompt: settings.SystemPrompt,
		},
		promptMessages,
		func(delta string) error {
			if err := writeStreamEvent(w, map[string]any{
				"type":    "delta",
				"content": delta,
			}); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		},
	)
	if err != nil {
		_ = writeStreamEvent(w, map[string]any{
			"type":  "error",
			"error": err.Error(),
		})
		flusher.Flush()
		return
	}

	assistantMessage, err := s.store.AddConversationMessage(r.Context(), user.ID, conversationID, "assistant", assistantText)
	if err != nil {
		_ = writeStreamEvent(w, map[string]any{
			"type":  "error",
			"error": "failed to persist assistant message",
		})
		flusher.Flush()
		return
	}

	conversation, err = s.store.GetConversationByID(r.Context(), user.ID, conversationID)
	if err != nil {
		_ = writeStreamEvent(w, map[string]any{
			"type":  "error",
			"error": "failed to refresh conversation",
		})
		flusher.Flush()
		return
	}

	_ = writeStreamEvent(w, map[string]any{
		"type":         "done",
		"conversation": conversation,
		"message":      assistantMessage,
	})
	flusher.Flush()
}

func (s *Server) guestChatStream(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Message string `json:"message"`
		History []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	message := strings.TrimSpace(body.Message)
	if message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	settings := s.defaultRuntimeSettings()
	if strings.TrimSpace(settings.BaseURL) == "" || strings.TrimSpace(settings.Models.Active) == "" {
		writeError(w, http.StatusBadRequest, "guest chat is not configured yet")
		return
	}

	used := s.readGuestUsage(r)
	if used >= guestChatLimit {
		writeError(w, http.StatusForbidden, "free guest chat limit reached, continue in workspace")
		return
	}
	used++
	if err := s.guests.SetUsage(w, used, 7*24*time.Hour); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist guest session")
		return
	}

	promptMessages := make([]map[string]string, 0, len(body.History)+1)
	for _, item := range body.History {
		role := strings.TrimSpace(strings.ToLower(item.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		promptMessages = append(promptMessages, map[string]string{
			"role":    role,
			"content": content,
		})
	}
	promptMessages = append(promptMessages, map[string]string{
		"role":    "user",
		"content": message,
	})

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if err := writeStreamEvent(w, map[string]any{
		"type":      "meta",
		"remaining": guestChatLimit - used,
		"limit":     guestChatLimit,
	}); err != nil {
		return
	}
	flusher.Flush()

	assistantText, err := s.aiClient.StreamChat(
		r.Context(),
		ai.RuntimeSettings{
			Provider:     settings.Provider,
			BaseURL:      settings.BaseURL,
			APIKey:       settings.APIKey,
			Model:        settings.Models.Active,
			SystemPrompt: settings.SystemPrompt,
		},
		promptMessages,
		func(delta string) error {
			if err := writeStreamEvent(w, map[string]any{
				"type":    "delta",
				"content": delta,
			}); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		},
	)
	if err != nil {
		_ = writeStreamEvent(w, map[string]any{
			"type":  "error",
			"error": err.Error(),
		})
		flusher.Flush()
		return
	}

	_ = writeStreamEvent(w, map[string]any{
		"type":      "done",
		"remaining": guestChatLimit - used,
		"limit":     guestChatLimit,
		"message": map[string]any{
			"role":      "assistant",
			"content":   assistantText,
			"createdAt": time.Now().UTC(),
		},
	})
	flusher.Flush()
}

func (s *Server) platformSubscription(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessBillingWrite) {
		writeError(w, http.StatusForbidden, "billing updates are not allowed for this role")
		return
	}

	var body struct {
		Platform         string          `json:"platform"`
		ProductID        string          `json:"productId"`
		Status           string          `json:"status"`
		CurrentPeriodEnd *time.Time      `json:"currentPeriodEnd"`
		RawPayload       json.RawMessage `json:"rawPayload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.Platform == "" || body.ProductID == "" || body.Status == "" {
		writeError(w, http.StatusBadRequest, "platform, productId, and status are required")
		return
	}

	if err := s.store.UpsertSubscription(r.Context(), user.ID, body.Platform, body.ProductID, body.Status, body.CurrentPeriodEnd, body.RawPayload); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update subscription")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) subscriptionWebhook(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read payload")
		return
	}
	if !validSignature(s.cfg.SubscriptionWebhookSecret, payload, r.Header.Get("X-Miaw-Signature")) {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	var body struct {
		UserID           string          `json:"userId"`
		Platform         string          `json:"platform"`
		ProductID        string          `json:"productId"`
		Status           string          `json:"status"`
		CurrentPeriodEnd *time.Time      `json:"currentPeriodEnd"`
		RawPayload       json.RawMessage `json:"rawPayload"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.UserID == "" || body.Platform == "" || body.ProductID == "" || body.Status == "" {
		writeError(w, http.StatusBadRequest, "userId, platform, productId, and status are required")
		return
	}

	raw := body.RawPayload
	if len(raw) == 0 {
		raw = payload
	}
	if err := s.store.UpsertSubscription(r.Context(), body.UserID, body.Platform, body.ProductID, body.Status, body.CurrentPeriodEnd, raw); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update subscription")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) requireUser(next func(http.ResponseWriter, *http.Request, models.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.SessionCookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}

		identity, err := s.sessions.ParseSession(cookie.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid session")
			return
		}

		user, err := s.store.GetUserByID(r.Context(), identity.UserID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "user not found")
			return
		}

		user = applySessionAccess(user, identity)
		next(w, r, user)
	}
}

const guestChatLimit = 5

func guestQuotaPayload(used int) map[string]int {
	if used < 0 {
		used = 0
	}
	if used > guestChatLimit {
		used = guestChatLimit
	}
	return map[string]int{
		"limit":     guestChatLimit,
		"used":      used,
		"remaining": guestChatLimit - used,
	}
}

func (s *Server) readGuestUsage(r *http.Request) int {
	cookie, err := r.Cookie(auth.GuestCookieName)
	if err != nil {
		return 0
	}

	used, err := s.guests.ParseUsage(cookie.Value)
	if err != nil {
		return 0
	}
	if used < 0 {
		return 0
	}
	if used > guestChatLimit {
		return guestChatLimit
	}
	return used
}

func (s *Server) buildAuthURL(provider string, state string) (string, error) {
	switch provider {
	case "google":
		if s.cfg.GoogleClientID == "" {
			return "", errors.New("GOOGLE_CLIENT_ID is not configured")
		}
		values := url.Values{}
		values.Set("client_id", s.cfg.GoogleClientID)
		values.Set("redirect_uri", s.cfg.APIBaseURL+"/v1/auth/google/callback")
		values.Set("response_type", "code")
		values.Set("scope", "openid email profile")
		values.Set("state", state)
		values.Set("prompt", "select_account")
		return "https://accounts.google.com/o/oauth2/v2/auth?" + values.Encode(), nil
	case "github":
		if s.cfg.GitHubClientID == "" {
			return "", errors.New("GITHUB_CLIENT_ID is not configured")
		}
		values := url.Values{}
		values.Set("client_id", s.cfg.GitHubClientID)
		values.Set("redirect_uri", s.cfg.APIBaseURL+"/v1/auth/github/callback")
		values.Set("scope", "read:user user:email")
		values.Set("state", state)
		return "https://github.com/login/oauth/authorize?" + values.Encode(), nil
	default:
		return "", errors.New("unsupported oauth provider")
	}
}

func (s *Server) exchangeOAuthCode(ctx context.Context, provider string, code string) (models.OAuthProfile, error) {
	switch provider {
	case "google":
		token, err := s.exchangeToken(ctx, "https://oauth2.googleapis.com/token", url.Values{
			"client_id":     {s.cfg.GoogleClientID},
			"client_secret": {s.cfg.GoogleClientSecret},
			"code":          {code},
			"grant_type":    {"authorization_code"},
			"redirect_uri":  {s.cfg.APIBaseURL + "/v1/auth/google/callback"},
		})
		if err != nil {
			return models.OAuthProfile{}, err
		}
		var profile struct {
			Sub     string `json:"sub"`
			Email   string `json:"email"`
			Name    string `json:"name"`
			Picture string `json:"picture"`
		}
		if err := s.getJSON(ctx, "https://www.googleapis.com/oauth2/v3/userinfo", token, &profile); err != nil {
			return models.OAuthProfile{}, err
		}
		return models.OAuthProfile{
			Provider:       "google",
			ProviderUserID: profile.Sub,
			Email:          profile.Email,
			Name:           profile.Name,
			AvatarURL:      profile.Picture,
		}, nil
	case "github":
		token, err := s.exchangeToken(ctx, "https://github.com/login/oauth/access_token", url.Values{
			"client_id":     {s.cfg.GitHubClientID},
			"client_secret": {s.cfg.GitHubClientSecret},
			"code":          {code},
			"redirect_uri":  {s.cfg.APIBaseURL + "/v1/auth/github/callback"},
		})
		if err != nil {
			return models.OAuthProfile{}, err
		}
		var profile struct {
			ID        int64  `json:"id"`
			Email     string `json:"email"`
			Name      string `json:"name"`
			Login     string `json:"login"`
			AvatarURL string `json:"avatar_url"`
		}
		if err := s.getJSON(ctx, "https://api.github.com/user", token, &profile); err != nil {
			return models.OAuthProfile{}, err
		}
		email := profile.Email
		if email == "" {
			email = fmt.Sprintf("%d+%s@users.noreply.github.com", profile.ID, profile.Login)
		}
		name := profile.Name
		if name == "" {
			name = profile.Login
		}
		return models.OAuthProfile{
			Provider:       "github",
			ProviderUserID: fmt.Sprint(profile.ID),
			Email:          email,
			Name:           name,
			AvatarURL:      profile.AvatarURL,
		}, nil
	default:
		return models.OAuthProfile{}, errors.New("unsupported oauth provider")
	}
}

func (s *Server) exchangeToken(ctx context.Context, endpoint string, values url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 || body.AccessToken == "" {
		if body.Error == "" {
			body.Error = "oauth token exchange failed"
		}
		return "", errors.New(body.Error)
	}
	return body.AccessToken, nil
}

func (s *Server) getJSON(ctx context.Context, endpoint string, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("oauth profile request failed with %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func validSignature(secret string, payload []byte, signature string) bool {
	if signature == "" {
		return false
	}
	signature = strings.TrimPrefix(signature, "sha256=")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

func writeStreamEvent(w http.ResponseWriter, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func (s *Server) loadRuntimeSettings(ctx context.Context, userID string) (models.RuntimeSettings, error) {
	settings, err := s.store.GetRuntimeSettings(ctx, userID)
	if err == nil {
		return settings, nil
	}
	if !database.IsNotFound(err) {
		return models.RuntimeSettings{}, err
	}
	return s.defaultRuntimeSettings(), nil
}

func (s *Server) defaultRuntimeSettings() models.RuntimeSettings {
	activeModel := firstModel(s.cfg.DefaultProviderModels)
	return models.RuntimeSettings{
		Provider:     firstNonEmpty(strings.TrimSpace(s.cfg.DefaultProvider), "openai"),
		BaseURL:      strings.TrimSpace(s.cfg.DefaultProviderBaseURL),
		APIKey:       strings.TrimSpace(s.cfg.DefaultProviderAPIKey),
		SystemPrompt: strings.TrimSpace(s.cfg.DefaultProviderSystem),
		Models: models.RuntimeModels{
			Active: activeModel,
			All:    append([]string(nil), s.cfg.DefaultProviderModels...),
		},
	}
}

func firstModel(models []string) string {
	for _, model := range models {
		if trimmed := strings.TrimSpace(model); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func titleFromMessage(message string) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(message)), " ")
	if normalized == "" {
		return "New chat"
	}
	if len(normalized) <= 48 {
		return normalized
	}
	return normalized[:45] + "..."
}
