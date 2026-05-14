package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"be-miawai/internal/ai"
	"be-miawai/internal/auth"
	"be-miawai/internal/config"
	"be-miawai/internal/database"
	"be-miawai/internal/models"
	"be-miawai/internal/payment"
	"be-miawai/internal/research"
	"be-miawai/internal/storage"

	"golang.org/x/crypto/bcrypt"
)

type Server struct {
	cfg             config.Config
	store           *database.Store
	sessions        *auth.Manager
	guests          *auth.GuestManager
	client          *http.Client
	aiClient        *ai.Client
	research        *research.Client
	chatStorage     storage.CloudStorage
	memoryWorkerMu  sync.Mutex
	chatGuardMu     sync.Mutex
	lastChatByUser  map[string]time.Time
	whatsAppRefresh func(context.Context, string) error
}

type errorResponse struct {
	Error string `json:"error"`
}

func NewServer(cfg config.Config, store *database.Store) *Server {
	server := &Server{
		cfg:      cfg,
		store:    store,
		sessions: auth.NewManager(cfg.SessionSecret, cfg.CookieSecure),
		guests:   auth.NewGuestManager(cfg.SessionSecret, cfg.CookieSecure),
		client:   &http.Client{Timeout: 12 * time.Second},
		aiClient: ai.NewClient(),
		research: research.NewClient(research.Config{
			Enabled:         cfg.WebResearchEnabled,
			SearxngURL:      cfg.SearxngURL,
			Timeout:         time.Duration(cfg.WebResearchTimeoutMs) * time.Millisecond,
			MaxResults:      cfg.WebResearchMaxResults,
			TargetPages:     cfg.WebResearchTargetPages,
			MaxContentChars: cfg.WebResearchMaxContentChars,
		}),
		chatStorage:    storage.NewLocalCloudStorage("storage/chats"),
		lastChatByUser: make(map[string]time.Time),
	}
	server.startMemoryExtractionWorker()
	return server
}

func (s *Server) SetWhatsAppPairingRefresher(refresh func(context.Context, string) error) {
	s.whatsAppRefresh = refresh
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/app/bootstrap", s.requireUser(s.bootstrap))
	mux.HandleFunc("GET /v1/guest/session", s.guestSession)
	mux.HandleFunc("POST /v1/guest/chat/stream", s.guestChatStream)
	mux.HandleFunc("POST /v1/auth/register", s.register)
	mux.HandleFunc("POST /v1/auth/login", s.passwordLogin)
	mux.HandleFunc("POST /v1/auth/dev-login", s.devLogin)
	mux.HandleFunc("GET /v1/auth/google/login", s.oauthLogin("google"))
	mux.HandleFunc("GET /v1/auth/github/login", s.oauthLogin("github"))
	mux.HandleFunc("GET /v1/auth/google/callback", s.oauthCallback("google"))
	mux.HandleFunc("GET /v1/auth/github/callback", s.oauthCallback("github"))
	mux.HandleFunc("POST /v1/auth/token-exchange", s.tokenExchange)
	mux.HandleFunc("POST /v1/auth/logout", s.logout)
	mux.HandleFunc("GET /v1/me", s.requireUser(s.me))
	mux.HandleFunc("GET /v1/runtime/settings", s.requireUser(s.runtimeSettings))
	mux.HandleFunc("PUT /v1/runtime/settings", s.requireUser(s.updateRuntimeSettings))
	mux.HandleFunc("POST /v1/runtime/settings/test", s.requireUser(s.testRuntimeSettings))
	mux.HandleFunc("GET /v1/usage/status", s.requireUser(s.usageStatus))
	mux.HandleFunc("GET /v1/whatsapp/accounts", s.requireUser(s.listWhatsAppAccounts))
	mux.HandleFunc("POST /v1/whatsapp/accounts", s.requireUser(s.createWhatsAppAccount))
	mux.HandleFunc("PATCH /v1/whatsapp/accounts/{id}", s.requireUser(s.updateWhatsAppAccount))
	mux.HandleFunc("POST /v1/whatsapp/accounts/{id}/pairing-refresh", s.requireUser(s.refreshWhatsAppPairing))
	mux.HandleFunc("DELETE /v1/whatsapp/accounts/{id}", s.requireUser(s.deleteWhatsAppAccount))
	mux.HandleFunc("POST /v1/whatsapp/link-codes", s.requireUser(s.createWhatsAppLinkCode))
	mux.HandleFunc("GET /v1/admin/overview", s.requireUser(s.adminOverview))
	mux.HandleFunc("GET /v1/admin/whatsapp/conversations", s.requireUser(s.adminWhatsAppConversations))
	mux.HandleFunc("GET /v1/admin/whatsapp/conversations/{id}", s.requireUser(s.adminWhatsAppConversation))
	mux.HandleFunc("GET /v1/admin/whatsapp/events", s.requireUser(s.adminWhatsAppEvents))
	mux.HandleFunc("GET /v1/admin/whatsapp/allow-list", s.requireUser(s.adminWhatsAppAllowList))
	mux.HandleFunc("POST /v1/admin/whatsapp/allow-list", s.requireUser(s.adminWhatsAppAllowContact))
	mux.HandleFunc("DELETE /v1/admin/whatsapp/allow-list/{id}", s.requireUser(s.adminWhatsAppRemoveAllowedContact))
	mux.HandleFunc("POST /v1/internal/whatsapp/inbound", s.whatsAppInbound)
	mux.HandleFunc("POST /v1/internal/whatsapp/accounts/{id}/status", s.whatsAppAccountStatus)
	mux.HandleFunc("POST /v1/research/search", s.requireUser(s.researchSearch))
	mux.HandleFunc("POST /v1/research/read-url", s.requireUser(s.researchReadURL))
	mux.HandleFunc("GET /v1/conversations", s.requireUser(s.listConversations))
	mux.HandleFunc("GET /v1/conversations/{id}", s.requireUser(s.getConversation))
	mux.HandleFunc("PATCH /v1/conversations/{id}", s.requireUser(s.updateConversation))
	mux.HandleFunc("DELETE /v1/conversations/{id}", s.requireUser(s.deleteConversation))
	mux.HandleFunc("GET /v1/memories", s.requireUser(s.listMemories))
	mux.HandleFunc("POST /v1/memories", s.requireUser(s.createMemory))
	mux.HandleFunc("PUT /v1/memories/{id}", s.requireUser(s.updateMemory))
	mux.HandleFunc("DELETE /v1/memories/{id}", s.requireUser(s.deleteMemory))
	mux.HandleFunc("GET /v1/trackers", s.requireUser(s.listTrackerEntries))
	mux.HandleFunc("POST /v1/trackers", s.requireUser(s.createTrackerEntry))
	mux.HandleFunc("PUT /v1/trackers/{id}", s.requireUser(s.updateTrackerEntry))
	mux.HandleFunc("DELETE /v1/trackers/{id}", s.requireUser(s.deleteTrackerEntry))
	mux.HandleFunc("GET /v1/tracker-suggestions", s.requireUser(s.listTrackerSuggestions))
	mux.HandleFunc("POST /v1/tracker-suggestions/{id}/approve", s.requireUser(s.approveTrackerSuggestion))
	mux.HandleFunc("POST /v1/tracker-suggestions/{id}/dismiss", s.requireUser(s.dismissTrackerSuggestion))
	mux.HandleFunc("GET /v1/chat/history", s.requireUser(s.chatHistory))
	mux.HandleFunc("POST /v1/chat", s.requireUser(s.chat))
	mux.HandleFunc("POST /v1/chat/stream", s.requireUser(s.chatStream))
	mux.HandleFunc("GET /v1/uploads/{id}", s.requireUser(s.serveUpload))
	mux.HandleFunc("GET /v1/subscriptions/entitlement", s.requireUser(s.me))
	mux.HandleFunc("POST /v1/subscriptions/trial", s.requireUser(s.startTrial))
	mux.HandleFunc("POST /v1/subscriptions/platform", s.requireUser(s.platformSubscription))
	mux.HandleFunc("POST /v1/webhooks/subscriptions", s.subscriptionWebhook)
	mux.HandleFunc("POST /v1/webhooks/midtrans", s.midtransWebhook)
	mux.HandleFunc("POST /v1/checkout/create", s.requireUser(s.createCheckout))
	mux.HandleFunc("POST /v1/checkout/sync", s.requireUser(s.syncCheckout))
	mux.HandleFunc("POST /v1/subscriptions/verify-android", s.requireUser(s.verifyAndroidPurchase))
	return s.withRequestLogging(mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessAdmin) {
		writeError(w, http.StatusForbidden, "admin access is required")
		return
	}

	windowDays := 7
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 90 {
			writeError(w, http.StatusBadRequest, "days must be between 1 and 90")
			return
		}
		windowDays = parsed
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}

	since := time.Now().Add(-time.Duration(windowDays) * 24 * time.Hour)
	overview, err := s.store.GetAdminOverview(r.Context(), since, limit)
	if err != nil {
		log.Printf("admin overview failed user_id=%s err=%v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to load admin overview")
		return
	}

	type usageRow struct {
		models.AdminUserUsage
		EstimatedCostUSD float64 `json:"estimatedCostUsd"`
	}
	usage := make([]usageRow, 0, len(overview.UsageByUser))
	for _, item := range overview.UsageByUser {
		cost := tokenCostUSD(item.TokenInput+item.TokenOutput, s.cfg.AICostUSDPer1KTokens)
		usage = append(usage, usageRow{AdminUserUsage: item, EstimatedCostUSD: cost})
	}
	totalCost := tokenCostUSD(overview.TotalTokenInput+overview.TotalTokenOutput, s.cfg.AICostUSDPer1KTokens)

	writeJSON(w, http.StatusOK, map[string]any{
		"windowDays":           windowDays,
		"generatedAt":          time.Now(),
		"totalUsers":           overview.TotalUsers,
		"activeSubscriptions":  overview.ActiveSubscriptions,
		"trialSubscriptions":   overview.TrialSubscriptions,
		"paymentTotalAmount":   overview.PaymentTotalAmount,
		"totalPromptCount":     overview.TotalPromptCount,
		"totalImageCount":      overview.TotalImageCount,
		"totalResearchCount":   overview.TotalResearchCount,
		"totalTokenInput":      overview.TotalTokenInput,
		"totalTokenOutput":     overview.TotalTokenOutput,
		"estimatedCostUsd":     totalCost,
		"usageByUser":          usage,
		"recentPayments":       overview.RecentPayments,
		"costUsdPer1KTokens":   s.cfg.AICostUSDPer1KTokens,
		"webResearchDailyFree": s.cfg.FreeUserDailyWebResearchLimit,
		"webResearchDailyPro":  s.cfg.ProUserDailyWebResearchLimit,
	})
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

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	email := normalizeEmail(body.Email)
	name := strings.TrimSpace(body.Name)
	if email == "" || !strings.Contains(email, "@") {
		writeError(w, http.StatusBadRequest, "valid email is required")
		return
	}
	if len(body.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if name == "" {
		name = strings.Split(email, "@")[0]
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to secure password")
		return
	}

	user, err := s.store.CreatePasswordUser(r.Context(), email, name, string(hash))
	if errors.Is(err, database.ErrEmailAlreadyExists) {
		writeError(w, http.StatusConflict, "email already registered")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to register account")
		return
	}
	s.writeUserSession(w, decorateUserAccess(user))
}

func (s *Server) passwordLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	account, err := s.store.FindPasswordAccountByEmail(r.Context(), normalizeEmail(body.Email))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sign in")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(body.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	s.writeUserSession(w, decorateUserAccess(account.User))
}

func (s *Server) writeUserSession(w http.ResponseWriter, user models.User) {
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

func (s *Server) startTrial(w http.ResponseWriter, r *http.Request, user models.User) {
	if strings.EqualFold(user.SubscriptionStatus, "active") || strings.EqualFold(user.SubscriptionStatus, "trialing") {
		writeError(w, http.StatusConflict, "Pro access is already active")
		return
	}
	if err := s.store.StartTrialSubscription(r.Context(), user.ID, 72*time.Hour); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "trial already used") {
			writeError(w, http.StatusConflict, "trial already used")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to start trial")
		return
	}
	updated, err := s.store.GetUserByID(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]models.User{"user": decorateUserAccess(updated)})
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

func (s *Server) tokenExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	profile, err := s.exchangeOAuthCode(r.Context(), body.Provider, body.Code)
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

	token, err := s.sessions.SignSession(auth.SessionIdentity{
		UserID: user.ID,
		Email:  user.Email,
		Role:   user.Role,
		Access: user.Access,
	}, 30*24*time.Hour)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sign session")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"user":  user,
	})
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

type usageStatusResponse struct {
	Plan          string              `json:"plan"`
	Subscription  string              `json:"subscription"`
	EntitledUntil *time.Time          `json:"entitledUntil"`
	Model         string              `json:"model"`
	Provider      string              `json:"provider"`
	Email         string              `json:"email"`
	Windows       []usageWindowStatus `json:"windows"`
	Credits       usageCreditStatus   `json:"credits"`
}

type usageWindowStatus struct {
	ID          string    `json:"id"`
	Label       string    `json:"label"`
	Used        int       `json:"used"`
	Limit       int       `json:"limit"`
	Remaining   int       `json:"remaining"`
	PercentLeft int       `json:"percentLeft"`
	ResetAt     time.Time `json:"resetAt"`
}

type usageCreditStatus struct {
	Initial   float64 `json:"initial"`
	Used      float64 `json:"used"`
	Remaining float64 `json:"remaining"`
}

func (s *Server) usageStatus(w http.ResponseWriter, r *http.Request, user models.User) {
	settings, _ := s.loadRuntimeSettings(r.Context(), user.ID)
	settings = applyRuntimeDefaults(settings)
	now := time.Now().In(wibLocation())
	fiveHourStart, fiveHourReset := currentFiveHourUsageWindow(now)
	weekStart, weekReset := currentWeeklyUsageWindow(now)
	fiveHourUsage, err := s.store.GetUsageWindow(r.Context(), user.ID, fiveHourStart)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load usage status")
		return
	}
	weeklyUsage, err := s.store.GetUsageWindow(r.Context(), user.ID, weekStart)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load usage status")
		return
	}
	fiveHourLimit := periodicTokenLimitForUser(s.cfg, user, "5h")
	weeklyLimit := periodicTokenLimitForUser(s.cfg, user, "week")
	weeklyTokenUsage := weeklyUsage.TokenInput + weeklyUsage.TokenOutput
	credits := buildUsageCreditStatus(includedCreditUSDForUser(s.cfg, user), weeklyTokenUsage, s.cfg.AICostUSDPer1KTokens)
	plan := strings.ToLower(firstNonEmpty(user.Plan, "free"))
	if plan == "pro" && user.SubscriptionStatus == "trialing" {
		plan = "pro_trial"
	}
	writeJSON(w, http.StatusOK, usageStatusResponse{
		Plan:          plan,
		Subscription:  user.SubscriptionStatus,
		EntitledUntil: user.EntitledUntil,
		Model:         settings.Models.Active,
		Provider:      settings.Provider,
		Email:         user.Email,
		Windows: []usageWindowStatus{
			buildUsageWindowStatus("5h", "5 hour usage limit", fiveHourUsage.TokenInput+fiveHourUsage.TokenOutput, fiveHourLimit, fiveHourReset),
			buildUsageWindowStatus("week", "Weekly usage limit", weeklyTokenUsage, weeklyLimit, weekReset),
		},
		Credits: credits,
	})
}

func (s *Server) researchSearch(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessChatRead) {
		writeError(w, http.StatusForbidden, "research access is not allowed for this role")
		return
	}
	var body struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	query := strings.TrimSpace(body.Query)
	if query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if _, err := s.checkUserQuota(r.Context(), user, 0, true); err != nil {
		writeError(w, http.StatusPaymentRequired, err.Error())
		return
	}
	startedAt := time.Now()
	log.Printf("research search start user=%s query=%q", user.ID, logValue(query, 160))
	report, err := s.research.SearchAndRead(r.Context(), query)
	if err != nil {
		log.Printf("research search failed user=%s query=%q durationMs=%d error=%q", user.ID, logValue(query, 160), time.Since(startedAt).Milliseconds(), err.Error())
		writeError(w, http.StatusBadGateway, "web research is temporarily unavailable")
		return
	}
	if err := s.store.IncrementDailyUsage(r.Context(), user.ID, 0, 0, 0, 1); err != nil {
		log.Printf("research usage tracking failed user=%s error=%q", user.ID, err.Error())
	}
	log.Printf("research search done user=%s query=%q results=%d pages=%d warnings=%d durationMs=%d", user.ID, logValue(query, 160), len(report.Results), len(report.Pages), len(report.Warnings), time.Since(startedAt).Milliseconds())
	writeJSON(w, http.StatusOK, map[string]research.Report{"report": report})
}

func (s *Server) researchReadURL(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessChatRead) {
		writeError(w, http.StatusForbidden, "research access is not allowed for this role")
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	rawURL := strings.TrimSpace(body.URL)
	if rawURL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	if !s.cfg.WebResearchEnabled {
		writeError(w, http.StatusBadRequest, "web research is disabled")
		return
	}
	if _, err := s.checkUserQuota(r.Context(), user, 0, true); err != nil {
		writeError(w, http.StatusPaymentRequired, err.Error())
		return
	}
	startedAt := time.Now()
	log.Printf("research read-url start user=%s url=%q", user.ID, logValue(rawURL, 220))
	page, err := s.research.ReadURL(r.Context(), rawURL)
	if err != nil {
		log.Printf("research read-url failed user=%s url=%q durationMs=%d error=%q", user.ID, logValue(rawURL, 220), time.Since(startedAt).Milliseconds(), err.Error())
		writeError(w, http.StatusBadGateway, "web research is temporarily unavailable")
		return
	}
	if err := s.store.IncrementDailyUsage(r.Context(), user.ID, 0, 0, 0, 1); err != nil {
		log.Printf("research usage tracking failed user=%s error=%q", user.ID, err.Error())
	}
	log.Printf("research read-url done user=%s url=%q finalUrl=%q status=%d chars=%d durationMs=%d", user.ID, logValue(rawURL, 220), logValue(page.FinalURL, 220), page.Status, len(page.TextPreview), time.Since(startedAt).Milliseconds())
	writeJSON(w, http.StatusOK, map[string]research.Report{"report": research.Report{
		Mode:    "url",
		URL:     rawURL,
		Pages:   []research.Page{page},
		TotalMs: time.Since(startedAt).Milliseconds(),
	}})
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
	conversation, err := s.store.GetConversationByID(r.Context(), user.ID, conversationID)
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load conversation metadata")
		return
	}

	messages, err := s.loadConversationMessages(r.Context(), user.ID, conversation)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load conversation history")
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

	conversation, err := s.store.UpdateConversationMeta(r.Context(), user.ID, conversationID, body.Title, body.Pinned, nil)
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
		writeError(w, http.StatusInternalServerError, "failed to delete conversation metadata")
		return
	}

	_ = s.chatStorage.DeleteMessages(conversationID)

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listTrackerEntries(w http.ResponseWriter, r *http.Request, user models.User) {
	module := r.URL.Query().Get("module")
	entries, err := s.store.ListTrackerEntries(r.Context(), user.ID, module)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load tracker entries")
		return
	}
	writeJSON(w, http.StatusOK, map[string][]models.TrackerEntry{"entries": entries})
}

func (s *Server) createTrackerEntry(w http.ResponseWriter, r *http.Request, user models.User) {
	var input models.TrackerEntryInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := validateTrackerInput(input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	entry, err := s.store.CreateTrackerEntry(r.Context(), user.ID, input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create tracker entry")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]models.TrackerEntry{"entry": entry})
}

func (s *Server) updateTrackerEntry(w http.ResponseWriter, r *http.Request, user models.User) {
	entryID := r.PathValue("id")
	var input models.TrackerEntryInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := validateTrackerInput(input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	entry, err := s.store.UpdateTrackerEntry(r.Context(), user.ID, entryID, input)
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "tracker entry not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update tracker entry")
		return
	}
	writeJSON(w, http.StatusOK, map[string]models.TrackerEntry{"entry": entry})
}

func (s *Server) deleteTrackerEntry(w http.ResponseWriter, r *http.Request, user models.User) {
	entryID := r.PathValue("id")
	if err := s.store.DeleteTrackerEntry(r.Context(), user.ID, entryID); err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "tracker entry not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete tracker entry")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listTrackerSuggestions(w http.ResponseWriter, r *http.Request, user models.User) {
	suggestions, err := s.store.ListTrackerSuggestions(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load tracker suggestions")
		return
	}
	writeJSON(w, http.StatusOK, map[string][]models.TrackerSuggestion{"suggestions": suggestions})
}

func (s *Server) approveTrackerSuggestion(w http.ResponseWriter, r *http.Request, user models.User) {
	entry, err := s.store.ApproveTrackerSuggestion(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "tracker suggestion not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to approve tracker suggestion")
		return
	}
	writeJSON(w, http.StatusOK, map[string]models.TrackerEntry{"entry": entry})
}

func (s *Server) dismissTrackerSuggestion(w http.ResponseWriter, r *http.Request, user models.User) {
	if err := s.store.DismissTrackerSuggestion(r.Context(), user.ID, r.PathValue("id")); err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "tracker suggestion not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to dismiss tracker suggestion")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) serveUpload(w http.ResponseWriter, r *http.Request, user models.User) {
	uploadID := filepath.Base(r.PathValue("id"))
	if uploadID == "." || uploadID == "" || strings.Contains(uploadID, "..") {
		writeError(w, http.StatusBadRequest, "invalid upload id")
		return
	}
	path := filepath.Join("storage", "uploads", user.ID, uploadID)
	http.ServeFile(w, r, path)
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
		ConversationID string                  `json:"conversationId"`
		Title          string                  `json:"title"`
		Message        string                  `json:"message"`
		Images         []models.ChatImageInput `json:"images"`
		Web            bool                    `json:"web"`
		SearchQuery    string                  `json:"searchQuery"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	message := strings.TrimSpace(body.Message)
	if message == "" && len(body.Images) == 0 {
		writeError(w, http.StatusBadRequest, "message or image is required")
		return
	}
	if s.cfg.MaxPromptChars > 0 && len([]rune(message)) > s.cfg.MaxPromptChars {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("message is too long, maximum %d characters", s.cfg.MaxPromptChars))
		return
	}
	if len(body.Images) > 4 {
		writeError(w, http.StatusBadRequest, "maximum 4 images per message")
		return
	}
	if err := s.checkPromptSpam(user.ID); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}

	settings, err := s.loadRuntimeSettings(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load runtime settings")
		return
	}
	settings = applyRuntimeDefaults(settings)

	// Substitute API key if using Managed AI
	if strings.TrimSpace(settings.APIKey) == "" {
		if s.cfg.ManagedAIApiKey != "" {
			settings.APIKey = s.cfg.ManagedAIApiKey
			if user.Plan != "pro" {
				settings.Models.Active = "gpt-4o-mini" // Force cheaper model for free tier
			}
		}
	}

	if strings.TrimSpace(settings.BaseURL) == "" || strings.TrimSpace(settings.Models.Active) == "" || strings.TrimSpace(settings.APIKey) == "" {
		writeError(w, http.StatusBadRequest, "runtime settings are incomplete or missing API key")
		return
	}

	webResearchAllowed := false
	if !isStatusCommand(message) {
		var err error
		webResearchAllowed, err = s.checkUserQuota(r.Context(), user, len(body.Images), body.Web)
		if err != nil {
			writeError(w, http.StatusPaymentRequired, err.Error())
			return
		}
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

	conversation, err := s.store.GetConversationByID(r.Context(), user.ID, conversationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to refresh conversation")
		return
	}

	messages, err := s.loadConversationMessages(r.Context(), user.ID, conversation)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load conversation history")
		return
	}

	savedImages, err := s.saveChatImages(r.Context(), user.ID, conversationID, "", body.Images)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	userMessage := newConversationMessage(conversationID, "user", message)
	userMessage.ImageURLs = publicURLs(savedImages)
	messages = append(messages, userMessage)
	for _, image := range savedImages {
		_ = s.store.InsertChatUpload(r.Context(), user.ID, conversationID, userMessage.ID, image.Name, image.MimeType, image.LocalPath, image.PublicURL, image.SizeBytes)
	}
	if err := s.chatStorage.SaveMessages(conversationID, messages); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync conversation history")
		return
	}
	if err := s.store.UpdateConversationStats(r.Context(), user.ID, conversationID, message, len(messages)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update conversation metadata")
		return
	}
	conversation, err = s.store.GetConversationByID(r.Context(), user.ID, conversationID)
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

	result, err := s.processChatReply(
		r.Context(),
		chatReplyInput{
			User:           user,
			Conversation:   conversation,
			Settings:       settings,
			Messages:       messages,
			UserMessage:    userMessage,
			SavedImages:    savedImages,
			Web:            body.Web,
			SearchQuery:    strings.TrimSpace(body.SearchQuery),
			WebAllowed:     webResearchAllowed,
			Stream:         true,
			ResearchLogTag: "chat",
			OnResearch: func(report *research.Report, researchErr string) error {
				payload := map[string]any{"type": "research"}
				if report != nil {
					payload["report"] = report
				}
				if researchErr != "" {
					payload["error"] = researchErr
				}
				if err := writeStreamEvent(w, payload); err != nil {
					return err
				}
				flusher.Flush()
				return nil
			},
			OnDelta: func(delta string) error {
				if err := writeStreamEvent(w, map[string]any{
					"type":    "delta",
					"content": delta,
				}); err != nil {
					return err
				}
				flusher.Flush()
				return nil
			},
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
		"type":         "done",
		"conversation": result.Conversation,
		"message":      result.AssistantMessage,
		"usage":        result.Usage,
	})
	flusher.Flush()
}

type chatReplyInput struct {
	User           models.User
	Conversation   models.Conversation
	Settings       models.RuntimeSettings
	Messages       []models.ConversationMessage
	UserMessage    models.ConversationMessage
	SavedImages    []savedChatImage
	Web            bool
	SearchQuery    string
	WebAllowed     bool
	Stream         bool
	ResearchLogTag string
	OnResearch     func(*research.Report, string) error
	OnDelta        func(string) error
}

type chatReplyResult struct {
	Conversation     models.Conversation
	AssistantMessage models.ConversationMessage
	Usage            ai.TokenUsage
	ResearchReport   *research.Report
}

func (s *Server) processChatReply(ctx context.Context, input chatReplyInput) (chatReplyResult, error) {
	message := strings.TrimSpace(input.UserMessage.Content)
	memoryContext := ""
	if memories, err := s.store.SearchMemories(ctx, input.User.ID, message, 8); err == nil {
		memoryContext = formatMemoryContext(memories)
	}
	trackerContext := ""
	if entries, err := s.loadTrackerContextEntries(ctx, input.User.ID, message); err == nil {
		trackerContext = formatTrackerContext(entries)
	}

	var webReport *research.Report
	webContext := ""
	webResearchError := ""
	webAttempted := false
	if input.WebAllowed {
		webReport, webContext, webResearchError, webAttempted = s.prepareWebResearchContext(
			ctx,
			input.Settings,
			input.User.ID,
			input.Conversation.ID,
			message,
			input.Web,
			input.SearchQuery,
			firstNonEmpty(input.ResearchLogTag, "chat"),
		)
	} else if input.Web && input.OnResearch != nil {
		webResearchError = "web research is not available on this plan"
		webAttempted = true
	}
	if webAttempted && input.OnResearch != nil {
		if err := input.OnResearch(webReport, webResearchError); err != nil {
			return chatReplyResult{}, err
		}
	}

	systemPrompt := systemPromptWithContexts(input.Settings.SystemPrompt, factualContextPolicy(), memoryContext, trackerContext, webContext)
	promptPayload := trimPromptMessagesForContext(
		promptMessagesFromConversationAny(input.Messages, input.SavedImages),
		s.cfg.ChatContextMessageLimit,
		s.cfg.ChatContextCharLimit,
	)

	if isStatusCommand(message) {
		assistantText := s.formatStatusReply(ctx, input.User, input.Settings, systemPrompt, promptPayload)
		assistantMessage := newConversationMessage(input.Conversation.ID, "assistant", assistantText)
		messages := append(input.Messages, assistantMessage)
		if err := s.chatStorage.SaveMessages(input.Conversation.ID, messages); err != nil {
			return chatReplyResult{}, errors.New("failed to persist assistant message")
		}
		if err := s.store.UpdateConversationStats(ctx, input.User.ID, input.Conversation.ID, assistantText, len(messages)); err != nil {
			return chatReplyResult{}, errors.New("failed to update conversation metadata")
		}
		conversation, err := s.store.GetConversationByID(ctx, input.User.ID, input.Conversation.ID)
		if err != nil {
			return chatReplyResult{}, errors.New("failed to refresh conversation")
		}
		return chatReplyResult{
			Conversation:     conversation,
			AssistantMessage: assistantMessage,
			Usage:            ai.TokenUsage{},
		}, nil
	}

	var chatResult ai.ChatResult
	var err error
	runtime := ai.RuntimeSettings{
		Provider:     input.Settings.Provider,
		BaseURL:      input.Settings.BaseURL,
		APIKey:       input.Settings.APIKey,
		Model:        input.Settings.Models.Active,
		SystemPrompt: systemPrompt,
	}
	if input.Stream {
		chatResult, err = s.aiClient.StreamChatAnyWithUsage(ctx, runtime, promptPayload, input.OnDelta)
	} else {
		reply, chatErr := s.aiClient.ChatAny(ctx, runtime, promptPayload)
		chatResult = ai.ChatResult{Content: strings.TrimSpace(reply)}
		err = chatErr
	}
	if err != nil {
		return chatReplyResult{}, err
	}

	assistantText := strings.TrimSpace(chatResult.Content)
	usage := chatResult.Usage
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		usage.PromptTokens = estimateTokensFromText(systemPrompt) + estimateTokensFromPromptMessages(promptPayload)
		usage.CompletionTokens = estimateTokensFromText(assistantText)
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	researchCount := 0
	if webAttempted && webResearchError == "" {
		researchCount = 1
	}
	_ = s.store.IncrementDailyUsage(ctx, input.User.ID, usage.PromptTokens, usage.CompletionTokens, len(input.SavedImages), researchCount)

	assistantMessage := newConversationMessage(input.Conversation.ID, "assistant", assistantText)
	if webReport != nil && webResearchError == "" {
		assistantMessage.ResearchReport = webReport
	}
	messages := append(input.Messages, assistantMessage)
	if err := s.chatStorage.SaveMessages(input.Conversation.ID, messages); err != nil {
		return chatReplyResult{}, errors.New("failed to persist assistant message")
	}
	if err := s.store.UpdateConversationStats(ctx, input.User.ID, input.Conversation.ID, assistantText, len(messages)); err != nil {
		return chatReplyResult{}, errors.New("failed to update conversation metadata")
	}

	s.extractMemoriesAsync(input.User.ID, input.Conversation.ID, input.Settings, input.UserMessage.Content, assistantMessage.Content)
	s.extractTrackersAsync(input.User.ID, input.Conversation.ID, input.UserMessage.ID, input.Settings, input.UserMessage.Content, input.SavedImages)

	conversation, err := s.store.GetConversationByID(ctx, input.User.ID, input.Conversation.ID)
	if err != nil {
		return chatReplyResult{}, errors.New("failed to refresh conversation")
	}
	return chatReplyResult{
		Conversation:     conversation,
		AssistantMessage: assistantMessage,
		Usage:            usage,
		ResearchReport:   webReport,
	}, nil
}

func (s *Server) prepareWebResearchContext(ctx context.Context, settings models.RuntimeSettings, userID string, conversationID string, message string, force bool, searchQuery string, logTag string) (*research.Report, string, string, bool) {
	webPlan := s.planWebResearch(ctx, settings, message, force, strings.TrimSpace(searchQuery))
	if !webPlan.NeedsResearch {
		log.Printf("%s web research skipped user=%s conversation=%s reason=%q", logTag, userID, conversationID, webPlan.Reason)
		return nil, "", "", false
	}
	if !s.cfg.WebResearchEnabled {
		errText := "web research is disabled"
		log.Printf("%s web research skipped user=%s conversation=%s reason=%q planReason=%q", logTag, userID, conversationID, errText, webPlan.Reason)
		return nil, "", errText, true
	}
	if webPlan.URL != "" {
		startedAt := time.Now()
		log.Printf("%s web research read-url start user=%s conversation=%s url=%q reason=%q", logTag, userID, conversationID, logValue(webPlan.URL, 220), webPlan.Reason)
		page, err := s.research.ReadURL(ctx, webPlan.URL)
		report := research.Report{
			Mode:    "url",
			URL:     webPlan.URL,
			Pages:   []research.Page{page},
			TotalMs: time.Since(startedAt).Milliseconds(),
		}
		if err != nil {
			log.Printf("%s web research read-url failed user=%s conversation=%s url=%q durationMs=%d error=%q", logTag, userID, conversationID, logValue(webPlan.URL, 220), report.TotalMs, err.Error())
			return &report, "", err.Error(), true
		}
		log.Printf("%s web research read-url done user=%s conversation=%s url=%q finalUrl=%q status=%d chars=%d durationMs=%d", logTag, userID, conversationID, logValue(webPlan.URL, 220), logValue(page.FinalURL, 220), page.Status, len(page.TextPreview), report.TotalMs)
		return &report, research.FormatContext(report), "", true
	}

	query := firstNonEmpty(webPlan.Query, message)
	startedAt := time.Now()
	log.Printf("%s web research search start user=%s conversation=%s query=%q reason=%q", logTag, userID, conversationID, logValue(query, 160), webPlan.Reason)
	report, err := s.research.SearchAndRead(ctx, query)
	if err != nil {
		log.Printf("%s web research search failed user=%s conversation=%s query=%q durationMs=%d error=%q", logTag, userID, conversationID, logValue(query, 160), time.Since(startedAt).Milliseconds(), err.Error())
		return &report, "", err.Error(), true
	}
	log.Printf("%s web research search done user=%s conversation=%s query=%q results=%d pages=%d warnings=%d durationMs=%d", logTag, userID, conversationID, logValue(query, 160), len(report.Results), len(report.Pages), len(report.Warnings), time.Since(startedAt).Milliseconds())
	return &report, research.FormatContext(report), "", true
}

func (s *Server) listMemories(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessChatRead) {
		writeError(w, http.StatusForbidden, "memory access is not allowed for this role")
		return
	}
	memories, err := s.store.ListMemoriesByUser(r.Context(), user.ID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load memories")
		return
	}
	writeJSON(w, http.StatusOK, map[string][]models.UserMemory{"memories": memories})
}

func (s *Server) createMemory(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessChatWrite) {
		writeError(w, http.StatusForbidden, "memory creation is not allowed for this role")
		return
	}
	var input models.MemoryInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(input.Content) == "" {
		writeError(w, http.StatusBadRequest, "memory content is required")
		return
	}
	memory, err := s.store.UpsertMemory(r.Context(), user.ID, input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save memory")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]models.UserMemory{"memory": memory})
}

func (s *Server) updateMemory(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessChatWrite) {
		writeError(w, http.StatusForbidden, "memory update is not allowed for this role")
		return
	}
	memoryID := strings.TrimSpace(r.PathValue("id"))
	if memoryID == "" {
		writeError(w, http.StatusBadRequest, "memory id is required")
		return
	}
	var input models.MemoryInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(input.Content) == "" {
		writeError(w, http.StatusBadRequest, "memory content is required")
		return
	}
	memory, err := s.store.UpdateMemory(r.Context(), user.ID, memoryID, input)
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "memory not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update memory")
		return
	}
	writeJSON(w, http.StatusOK, map[string]models.UserMemory{"memory": memory})
}

func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessChatWrite) {
		writeError(w, http.StatusForbidden, "memory deletion is not allowed for this role")
		return
	}
	memoryID := strings.TrimSpace(r.PathValue("id"))
	if memoryID == "" {
		writeError(w, http.StatusBadRequest, "memory id is required")
		return
	}
	if err := s.store.DeleteMemory(r.Context(), user.ID, memoryID); err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "memory not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete memory")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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

func (s *Server) createCheckout(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		ProductID string `json:"productId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	amount, ok := checkoutProductAmount(body.ProductID)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid productId")
		return
	}

	orderID := "MIAW-" + database.NewID("ord")
	midtrans := payment.MidtransService{Config: s.cfg}
	if strings.TrimSpace(s.cfg.MidtransClientKey) == "" {
		writeError(w, http.StatusServiceUnavailable, "payment popup is not configured")
		return
	}
	returnURL := s.cfg.AppBaseURL + "/workspace?checkout=midtrans&order_id=" + url.QueryEscape(orderID)
	snapResp, err := midtrans.CreateSnapTransaction(orderID, body.ProductID, amount, user.Email, user.Name, returnURL)
	if err != nil {
		log.Printf("create checkout failed: %v", err)
		writeError(w, http.StatusServiceUnavailable, "payment is not available yet")
		return
	}

	txn := models.PaymentTransaction{
		UserID:    user.ID,
		OrderID:   orderID,
		Platform:  "midtrans",
		ProductID: body.ProductID,
		Amount:    amount,
		Currency:  "IDR",
		Status:    "pending",
	}
	if err := s.store.CreatePaymentTransaction(r.Context(), txn); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create payment transaction")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"orderId":     orderID,
		"orderLabel":  orderID,
		"productId":   body.ProductID,
		"amount":      amount,
		"currency":    "IDR",
		"token":       snapResp.Token,
		"redirectUrl": snapResp.RedirectURL,
		"snapJsUrl":   midtrans.SnapJSScriptURL(),
		"clientKey":   s.cfg.MidtransClientKey,
	})
}

func (s *Server) syncCheckout(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		OrderID string `json:"orderId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	orderID := strings.TrimSpace(body.OrderID)
	if orderID == "" || !strings.HasPrefix(orderID, "MIAW-") {
		writeError(w, http.StatusBadRequest, "valid orderId is required")
		return
	}

	txn, err := s.store.GetPaymentByOrderID(r.Context(), orderID)
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "transaction not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load transaction")
		return
	}
	if txn.UserID != user.ID {
		writeError(w, http.StatusForbidden, "transaction does not belong to this user")
		return
	}

	midtrans := payment.MidtransService{Config: s.cfg}
	statusResp, err := midtrans.GetTransactionStatus(orderID)
	if err != nil {
		log.Printf("sync checkout failed orderID=%s: %v", orderID, err)
		writeError(w, http.StatusBadGateway, "failed to sync payment status")
		return
	}

	status := normalizeMidtransStatus(statusResp.TransactionStatus, statusResp.FraudStatus)
	if status == "" {
		status = "pending"
	}
	if err := s.store.UpdatePaymentTransactionStatus(r.Context(), orderID, status); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update transaction")
		return
	}

	if (status == "settlement" || status == "capture") && isSubscriptionProduct(txn.ProductID) {
		currentPeriodEnd := subscriptionPeriodEnd(txn.ProductID)
		raw, _ := json.Marshal(statusResp)
		if err := s.store.UpsertSubscription(r.Context(), txn.UserID, "midtrans", txn.ProductID, "active", &currentPeriodEnd, raw); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to activate subscription")
			return
		}
	}

	refreshedUser, err := s.store.GetUserByID(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to refresh account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"status": status,
		"user":   decorateUserAccess(refreshedUser),
	})
}

func (s *Server) verifyAndroidPurchase(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		PurchaseToken string `json:"purchaseToken"`
		ProductID     string `json:"productId"`
		PackageName   string `json:"packageName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	if body.PackageName != s.cfg.GooglePlayPackageName {
		writeError(w, http.StatusBadRequest, "invalid package name")
		return
	}

	gp := payment.GooglePlayService{Config: s.cfg}
	res, err := gp.VerifyPurchaseToken(r.Context(), body.ProductID, body.PurchaseToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to verify purchase: "+err.Error())
		return
	}

	if !res.IsValid {
		writeError(w, http.StatusBadRequest, "invalid purchase token")
		return
	}

	txn := models.PaymentTransaction{
		UserID:    user.ID,
		OrderID:   body.PurchaseToken,
		Platform:  "google_play",
		ProductID: body.ProductID,
		Amount:    0,
		Currency:  "IDR",
		Status:    "settlement",
	}
	_ = s.store.CreatePaymentTransaction(r.Context(), txn)

	if err := s.store.UpsertSubscription(r.Context(), user.ID, "google_play", body.ProductID, res.Status, res.CurrentPeriodEnd, []byte(`{}`)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update subscription")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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

func (s *Server) midtransWebhook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OrderID           string `json:"order_id"`
		TransactionStatus string `json:"transaction_status"`
		FraudStatus       string `json:"fraud_status"`
		StatusCode        string `json:"status_code"`
		GrossAmount       string `json:"gross_amount"`
		SignatureKey      string `json:"signature_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if !strings.HasPrefix(body.OrderID, "MIAW-") {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}

	midtrans := payment.MidtransService{Config: s.cfg}
	if !midtrans.VerifySignatureKey(body.OrderID, body.StatusCode, body.GrossAmount, body.SignatureKey) {
		writeError(w, http.StatusUnauthorized, "invalid midtrans signature")
		return
	}

	txn, err := s.store.GetPaymentByOrderID(r.Context(), body.OrderID)
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "transaction not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load transaction")
		return
	}

	status := normalizeMidtransStatus(body.TransactionStatus, body.FraudStatus)
	if err := s.store.UpdatePaymentTransactionStatus(r.Context(), body.OrderID, status); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update transaction")
		return
	}

	if (status == "settlement" || status == "capture") && isSubscriptionProduct(txn.ProductID) {
		currentPeriodEnd := subscriptionPeriodEnd(txn.ProductID)
		raw, _ := json.Marshal(body)
		if err := s.store.UpsertSubscription(r.Context(), txn.UserID, "midtrans", txn.ProductID, "active", &currentPeriodEnd, raw); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to activate subscription")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) requireUser(next func(http.ResponseWriter, *http.Request, models.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sessionToken string
		if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
			sessionToken = strings.TrimPrefix(authHeader, "Bearer ")
		} else if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
			sessionToken = cookie.Value
		}

		if sessionToken == "" {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}

		identity, err := s.sessions.ParseSession(sessionToken)
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

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(payload []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(payload)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *Server) withRequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = database.NewID("req")
		}
		w.Header().Set("X-Request-ID", requestID)
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("request panic request_id=%s method=%s path=%s err=%v", requestID, r.Method, r.URL.Path, recovered)
				writeError(recorder, http.StatusInternalServerError, "unexpected server error")
			}
			log.Printf(
				"request request_id=%s method=%s path=%s status=%d duration_ms=%d remote=%s ua=%q",
				requestID,
				r.Method,
				r.URL.Path,
				recorder.status,
				time.Since(start).Milliseconds(),
				r.RemoteAddr,
				r.UserAgent(),
			)
		}()

		next.ServeHTTP(recorder, r)
	})
}

func (s *Server) checkPromptSpam(userID string) error {
	if s.cfg.MinChatIntervalMs <= 0 {
		return nil
	}
	now := time.Now()
	minInterval := time.Duration(s.cfg.MinChatIntervalMs) * time.Millisecond
	s.chatGuardMu.Lock()
	defer s.chatGuardMu.Unlock()
	if previous, ok := s.lastChatByUser[userID]; ok && now.Sub(previous) < minInterval {
		return fmt.Errorf("please wait %.1f seconds before sending another message", minInterval.Seconds())
	}
	s.lastChatByUser[userID] = now
	return nil
}

func tokenCostUSD(tokens int, costPer1K float64) float64 {
	if tokens <= 0 || costPer1K <= 0 {
		return 0
	}
	return roundCurrency((float64(tokens) / 1000) * costPer1K)
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
	return applyRuntimeDefaults(models.RuntimeSettings{
		Provider:     firstNonEmpty(strings.TrimSpace(s.cfg.DefaultProvider), "openai"),
		BaseURL:      strings.TrimSpace(s.cfg.DefaultProviderBaseURL),
		APIKey:       strings.TrimSpace(s.cfg.DefaultProviderAPIKey),
		SystemPrompt: strings.TrimSpace(s.cfg.DefaultProviderSystem),
		Models: models.RuntimeModels{
			Active: activeModel,
			All:    append([]string(nil), s.cfg.DefaultProviderModels...),
		},
	})
}

func applyRuntimeDefaults(settings models.RuntimeSettings) models.RuntimeSettings {
	if strings.TrimSpace(settings.Provider) == "" {
		settings.Provider = "openai"
	}
	if strings.EqualFold(strings.TrimSpace(settings.Provider), "openai") && strings.TrimSpace(settings.BaseURL) == "" {
		settings.BaseURL = "https://api.openai.com/v1"
	}
	return settings
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

func validateTrackerInput(input models.TrackerEntryInput) error {
	module := strings.ToLower(strings.TrimSpace(input.Module))
	switch module {
	case "finance", "assets", "pangan", "health", "persona":
	default:
		return errors.New("module must be one of finance, assets, pangan, health, or persona")
	}
	if strings.TrimSpace(input.Title) == "" {
		return errors.New("title is required")
	}
	if len(strings.TrimSpace(input.Title)) > 160 {
		return errors.New("title is too long")
	}
	if len(strings.TrimSpace(input.Detail)) > 2000 {
		return errors.New("detail is too long")
	}
	return nil
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

func newConversationMessage(conversationID string, role string, content string) models.ConversationMessage {
	return models.ConversationMessage{
		ID:             database.NewID("cmsg"),
		ConversationID: conversationID,
		Role:           role,
		Content:        content,
		CreatedAt:      time.Now().UTC(),
	}
}

func (s *Server) loadConversationMessages(ctx context.Context, userID string, conversation models.Conversation) ([]models.ConversationMessage, error) {
	messages, err := s.chatStorage.GetMessages(conversation.ID)
	if err != nil {
		return nil, err
	}
	if len(messages) > 0 || conversation.MessageCount == 0 {
		return messages, nil
	}

	messages, err = s.store.ListLegacyConversationMessages(ctx, userID, conversation.ID)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return messages, nil
	}

	if err := s.chatStorage.SaveMessages(conversation.ID, messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func promptMessagesFromConversation(messages []models.ConversationMessage) []map[string]string {
	promptMessages := make([]map[string]string, 0, len(messages))
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role != "user" && role != "assistant" && role != "system" {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		promptMessages = append(promptMessages, map[string]string{
			"role":    role,
			"content": content,
		})
	}
	return promptMessages
}

type savedChatImage struct {
	Name      string
	MimeType  string
	LocalPath string
	PublicURL string
	SizeBytes int
	DataURL   string
}

func (s *Server) saveChatImages(ctx context.Context, userID string, conversationID string, messageID string, images []models.ChatImageInput) ([]savedChatImage, error) {
	if len(images) == 0 {
		return nil, nil
	}
	dir := filepath.Join("storage", "uploads", userID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, errors.New("failed to prepare upload storage")
	}

	saved := make([]savedChatImage, 0, len(images))
	for _, image := range images {
		mimeType := strings.ToLower(strings.TrimSpace(image.MimeType))
		if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/webp" {
			return nil, errors.New("only png, jpeg, or webp images are supported")
		}
		raw, err := hexOrBase64Decode(strings.TrimSpace(image.DataBase64))
		if err != nil {
			return nil, errors.New("invalid image data")
		}
		if len(raw) == 0 || len(raw) > 8*1024*1024 {
			return nil, errors.New("image must be between 1 byte and 8 MB")
		}
		ext := ".png"
		if mimeType == "image/jpeg" {
			ext = ".jpg"
		} else if mimeType == "image/webp" {
			ext = ".webp"
		}
		fileName := database.NewID("img") + ext
		localPath := filepath.Join(dir, fileName)
		if err := os.WriteFile(localPath, raw, 0644); err != nil {
			return nil, errors.New("failed to save image")
		}
		publicURL := "/v1/uploads/" + fileName
		saved = append(saved, savedChatImage{
			Name:      strings.TrimSpace(image.Name),
			MimeType:  mimeType,
			LocalPath: localPath,
			PublicURL: publicURL,
			SizeBytes: len(raw),
			DataURL:   "data:" + mimeType + ";base64," + strings.TrimSpace(image.DataBase64),
		})
	}
	return saved, nil
}

func publicURLs(images []savedChatImage) []string {
	urls := make([]string, 0, len(images))
	for _, image := range images {
		urls = append(urls, image.PublicURL)
	}
	return urls
}

func promptMessagesFromConversationAny(messages []models.ConversationMessage, latestImages []savedChatImage) []map[string]any {
	promptMessages := make([]map[string]any, 0, len(messages))
	for index, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role != "user" && role != "assistant" && role != "system" {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" && len(message.ImageURLs) == 0 && !(index == len(messages)-1 && len(latestImages) > 0) {
			continue
		}
		if role == "user" && index == len(messages)-1 && len(latestImages) > 0 {
			parts := []map[string]any{}
			if content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": content})
			}
			for _, image := range latestImages {
				parts = append(parts, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": image.DataURL,
					},
				})
			}
			promptMessages = append(promptMessages, map[string]any{"role": role, "content": parts})
			continue
		}
		promptMessages = append(promptMessages, map[string]any{"role": role, "content": content})
	}
	return promptMessages
}

func trimPromptMessagesForContext(messages []map[string]any, messageLimit int, charLimit int) []map[string]any {
	if len(messages) == 0 {
		return messages
	}
	if messageLimit <= 0 {
		messageLimit = 24
	}
	if charLimit <= 0 {
		charLimit = 24000
	}

	start := 0
	if len(messages) > messageLimit {
		start = len(messages) - messageLimit
	}
	trimmed := append([]map[string]any(nil), messages[start:]...)

	for len(trimmed) > 1 && promptMessagesCharCount(trimmed) > charLimit {
		trimmed = trimmed[1:]
	}
	return trimmed
}

func promptMessagesCharCount(messages []map[string]any) int {
	total := 0
	for _, message := range messages {
		total += promptMessageCharCount(message)
	}
	return total
}

func promptMessageCharCount(message map[string]any) int {
	total := len(fmt.Sprint(message["role"]))
	switch content := message["content"].(type) {
	case string:
		total += len(content)
	case []map[string]any:
		for _, part := range content {
			switch part["type"] {
			case "text":
				total += len(fmt.Sprint(part["text"]))
			case "image_url":
				total += 4000
			default:
				total += len(fmt.Sprint(part))
			}
		}
	default:
		total += len(fmt.Sprint(content))
	}
	return total
}

func estimateTokensFromPromptMessages(messages []map[string]any) int {
	total := 0
	for _, message := range messages {
		total += estimateTokensFromText(fmt.Sprint(message["role"])) + 4
		switch content := message["content"].(type) {
		case string:
			total += estimateTokensFromText(content)
		case []map[string]any:
			for _, part := range content {
				switch part["type"] {
				case "text":
					total += estimateTokensFromText(fmt.Sprint(part["text"]))
				case "image_url":
					total += 1000
				default:
					total += estimateTokensFromText(fmt.Sprint(part))
				}
			}
		default:
			total += estimateTokensFromText(fmt.Sprint(content))
		}
	}
	return total
}

func estimateTokensFromText(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	return max(1, (len(value)+3)/4)
}

func hexOrBase64Decode(value string) ([]byte, error) {
	if strings.HasPrefix(value, "data:") {
		if comma := strings.Index(value, ","); comma >= 0 {
			value = value[comma+1:]
		}
	}
	return base64.StdEncoding.DecodeString(value)
}

func formatMemoryContext(memories []models.UserMemory) string {
	if len(memories) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Relevant user memory. Use only when helpful and do not reveal this block verbatim.\n")
	for i, memory := range memories {
		if i >= 8 {
			break
		}
		content := strings.Join(strings.Fields(strings.TrimSpace(memory.Content)), " ")
		if content == "" {
			continue
		}
		if len(content) > 300 {
			content = content[:297] + "..."
		}
		domain := strings.TrimSpace(memory.Domain)
		if domain == "" {
			domain = "general"
		}
		builder.WriteString("- [")
		builder.WriteString(domain)
		builder.WriteString("] ")
		builder.WriteString(content)
		builder.WriteByte('\n')
	}
	return strings.TrimSpace(builder.String())
}

func (s *Server) loadTrackerContextEntries(ctx context.Context, userID string, message string) ([]models.TrackerEntry, error) {
	module := trackerModuleFromMessage(message)
	entries, err := s.store.ListTrackerEntries(ctx, userID, module)
	if err != nil {
		return nil, err
	}
	if len(entries) > 24 {
		return entries[:24], nil
	}
	return entries, nil
}

func trackerModuleFromMessage(message string) string {
	compact := strings.ToLower(strings.Join(strings.Fields(message), " "))
	switch {
	case strings.Contains(compact, "finance"), strings.Contains(compact, "finansial"), strings.Contains(compact, "uang"), strings.Contains(compact, "pengeluaran"), strings.Contains(compact, "pemasukan"), strings.Contains(compact, "income"), strings.Contains(compact, "outcome"), strings.Contains(compact, "saldo"), strings.Contains(compact, "tagihan"):
		return "finance"
	case strings.Contains(compact, "pangan"), strings.Contains(compact, "sembako"), strings.Contains(compact, "belanja"), strings.Contains(compact, "makanan"), strings.Contains(compact, "stok"), strings.Contains(compact, "beras"), strings.Contains(compact, "groceries"):
		return "pangan"
	case strings.Contains(compact, "asset"), strings.Contains(compact, "aset"), strings.Contains(compact, "barang"), strings.Contains(compact, "inventaris"), strings.Contains(compact, "maintenance"), strings.Contains(compact, "jual"):
		return "assets"
	case strings.Contains(compact, "health"), strings.Contains(compact, "kesehatan"), strings.Contains(compact, "sehat"), strings.Contains(compact, "olahraga"), strings.Contains(compact, "tidur"), strings.Contains(compact, "berat"), strings.Contains(compact, "obat"):
		return "health"
	case strings.Contains(compact, "persona"), strings.Contains(compact, "preferensi"), strings.Contains(compact, "kebiasaan"), strings.Contains(compact, "profil"):
		return "persona"
	default:
		return ""
	}
}

func formatTrackerContext(entries []models.TrackerEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Miaw tracker data from the user's saved entries. Treat this as the only source of truth for finance/pangan/assets/health/persona facts. If the needed fact is not present here, say the tracker data does not contain it yet.\n")
	for i, entry := range entries {
		if i >= 24 {
			break
		}
		builder.WriteString("- [")
		builder.WriteString(strings.TrimSpace(entry.Module))
		builder.WriteString("] ")
		builder.WriteString(compactTrackerField(entry.Title, 120))
		if value := compactTrackerField(entry.Amount, 80); value != "" {
			builder.WriteString(" | amount: ")
			builder.WriteString(value)
		}
		if value := compactTrackerField(entry.Status, 80); value != "" {
			builder.WriteString(" | status: ")
			builder.WriteString(value)
		}
		if value := compactTrackerField(entry.Category, 80); value != "" {
			builder.WriteString(" | category: ")
			builder.WriteString(value)
		}
		if value := compactTrackerField(entry.Detail, 220); value != "" {
			builder.WriteString(" | detail: ")
			builder.WriteString(value)
		}
		builder.WriteString(" | updated: ")
		builder.WriteString(entry.UpdatedAt.Format("2006-01-02"))
		if metadata := compactTrackerMetadata(entry.Metadata); metadata != "" {
			builder.WriteString(" | metadata: ")
			builder.WriteString(metadata)
		}
		builder.WriteByte('\n')
	}
	return strings.TrimSpace(builder.String())
}

func compactTrackerField(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return ""
	}
	if limit > 3 && len(value) > limit {
		return value[:limit-3] + "..."
	}
	return value
}

func compactTrackerMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	keys := []string{"date", "currency", "amountNumber", "quantity", "unit", "condition", "confidence"}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value, ok := metadata[key]
		if !ok {
			continue
		}
		text := compactTrackerField(fmt.Sprint(value), 80)
		if text == "" {
			continue
		}
		parts = append(parts, key+"="+text)
	}
	return strings.Join(parts, ", ")
}

func isStatusCommand(message string) bool {
	switch strings.ToLower(strings.TrimSpace(message)) {
	case "/status", "status", "/usage", "usage":
		return true
	default:
		return false
	}
}

func (s *Server) formatStatusReply(ctx context.Context, user models.User, settings models.RuntimeSettings, systemPrompt string, promptPayload []map[string]any) string {
	usage, err := s.store.GetDailyUsage(ctx, user.ID)
	if err != nil {
		return "Miaw Ecosystem status belum bisa dimuat: " + err.Error()
	}
	now := time.Now().In(wibLocation())
	resetAt := nextDailyReset(now)
	contextTokens := estimateTokensFromText(systemPrompt) + estimateTokensFromPromptMessages(promptPayload)
	contextLimit := max(1, s.cfg.ChatContextCharLimit/4)
	contextPercent := min(100, (contextTokens*100)/contextLimit)
	inputTokens := usage.TokenInput
	outputTokens := usage.TokenOutput
	totalTokens := inputTokens + outputTokens

	planLabel := strings.ToUpper(firstNonEmpty(user.Plan, "free"))
	if user.Plan == "pro" && user.SubscriptionStatus == "trialing" {
		planLabel = "PRO TRIAL"
	}
	entitlement := "none"
	if user.EntitledUntil != nil {
		entitlement = user.EntitledUntil.In(wibLocation()).Format("2006-01-02 15:04 MST")
	}

	chatLimit, chatRemaining := limitStatus(usage.PromptCount, dailyPromptLimitForUser(s.cfg, user))
	imageLimit, imageRemaining := limitStatus(usage.ImageCount, dailyImageLimitForUser(s.cfg, user))
	researchLimit, researchRemaining := limitStatus(usage.ResearchCount, dailyResearchLimitForUser(s.cfg, user))

	var builder strings.Builder
	builder.WriteString("Miaw Ecosystem status\n")
	builder.WriteString("Model: ")
	builder.WriteString(firstNonEmpty(settings.Models.Active, "not configured"))
	builder.WriteString(" · Provider: ")
	builder.WriteString(firstNonEmpty(settings.Provider, "not configured"))
	builder.WriteString(" · Login: ")
	builder.WriteString(firstNonEmpty(user.Email, "unknown"))
	builder.WriteByte('\n')
	builder.WriteString("Plan: ")
	builder.WriteString(planLabel)
	builder.WriteString(" · Subscription: ")
	builder.WriteString(firstNonEmpty(user.SubscriptionStatus, "none"))
	builder.WriteString(" · Entitled until: ")
	builder.WriteString(entitlement)
	builder.WriteByte('\n')
	builder.WriteString("Tokens today: ")
	builder.WriteString(formatCompactNumber(totalTokens))
	builder.WriteString(" total · ")
	builder.WriteString(formatCompactNumber(inputTokens))
	builder.WriteString(" in / ")
	builder.WriteString(formatCompactNumber(outputTokens))
	builder.WriteString(" out")
	builder.WriteByte('\n')
	builder.WriteString("Context: ")
	builder.WriteString(formatCompactNumber(contextTokens))
	builder.WriteByte('/')
	builder.WriteString(formatCompactNumber(contextLimit))
	builder.WriteString(" tokens (")
	builder.WriteString(fmtInt(contextPercent))
	builder.WriteString("%)")
	builder.WriteByte('\n')
	builder.WriteString("Usage today: chat ")
	builder.WriteString(fmtInt(usage.PromptCount))
	builder.WriteByte('/')
	builder.WriteString(chatLimit)
	builder.WriteString(" (")
	builder.WriteString(chatRemaining)
	builder.WriteString(" left)")
	builder.WriteString(" · images ")
	builder.WriteString(fmtInt(usage.ImageCount))
	builder.WriteByte('/')
	builder.WriteString(imageLimit)
	builder.WriteString(" (")
	builder.WriteString(imageRemaining)
	builder.WriteString(" left)")
	builder.WriteString(" · web ")
	builder.WriteString(fmtInt(usage.ResearchCount))
	builder.WriteByte('/')
	builder.WriteString(researchLimit)
	builder.WriteString(" (")
	builder.WriteString(researchRemaining)
	builder.WriteString(" left)")
	builder.WriteByte('\n')
	builder.WriteString("Daily reset: ")
	builder.WriteString(resetAt.Format("2006-01-02 15:04 MST"))
	builder.WriteString(" · ")
	builder.WriteString(formatDuration(resetAt.Sub(now)))
	builder.WriteString(" left")
	return builder.String()
}

func dailyPromptLimitForUser(cfg config.Config, user models.User) int {
	if user.Plan == "pro" {
		return cfg.ProUserDailyPromptLimit
	}
	return cfg.FreeUserDailyPromptLimit
}

func dailyImageLimitForUser(cfg config.Config, user models.User) int {
	if user.Plan == "pro" {
		return cfg.ProUserDailyImageLimit
	}
	return cfg.FreeUserDailyImageLimit
}

func dailyResearchLimitForUser(cfg config.Config, user models.User) int {
	if user.Plan == "pro" {
		return cfg.ProUserDailyWebResearchLimit
	}
	return cfg.FreeUserDailyWebResearchLimit
}

func periodicTokenLimitForUser(cfg config.Config, user models.User, window string) int {
	if user.Plan == "pro" {
		if window == "week" {
			return cfg.ProUserWeeklyTokenLimit
		}
		return cfg.ProUserFiveHourTokenLimit
	}
	if window == "week" {
		return cfg.FreeUserWeeklyTokenLimit
	}
	return cfg.FreeUserFiveHourTokenLimit
}

func includedCreditUSDForUser(cfg config.Config, user models.User) float64 {
	if user.Plan == "pro" {
		return cfg.ProUserIncludedCreditUSD
	}
	return cfg.FreeUserIncludedCreditUSD
}

func buildUsageWindowStatus(id string, label string, used int, limit int, resetAt time.Time) usageWindowStatus {
	remaining := 0
	percentLeft := 100
	if limit > 0 {
		remaining = max(0, limit-used)
		percentLeft = max(0, min(100, (remaining*100)/limit))
	}
	return usageWindowStatus{
		ID:          id,
		Label:       label,
		Used:        used,
		Limit:       limit,
		Remaining:   remaining,
		PercentLeft: percentLeft,
		ResetAt:     resetAt,
	}
}

func buildUsageCreditStatus(initialCreditUSD float64, usedTokens int, costUSDPer1KTokens float64) usageCreditStatus {
	used := 0.0
	if costUSDPer1KTokens > 0 && usedTokens > 0 {
		used = (float64(usedTokens) / 1000) * costUSDPer1KTokens
	}
	return usageCreditStatus{
		Initial:   roundCurrency(maxFloat(0, initialCreditUSD)),
		Used:      roundCurrency(maxFloat(0, used)),
		Remaining: roundCurrency(maxFloat(0, initialCreditUSD-used)),
	}
}

func (s *Server) hasRemainingWeeklyUsageCredit(ctx context.Context, user models.User) (bool, error) {
	includedCreditUSD := includedCreditUSDForUser(s.cfg, user)
	if includedCreditUSD <= 0 || s.cfg.AICostUSDPer1KTokens <= 0 {
		return true, nil
	}
	now := time.Now().In(wibLocation())
	weekStart, _ := currentWeeklyUsageWindow(now)
	weeklyUsage, err := s.store.GetUsageWindow(ctx, user.ID, weekStart)
	if err != nil {
		return false, err
	}
	weeklyTokenUsage := weeklyUsage.TokenInput + weeklyUsage.TokenOutput
	credits := buildUsageCreditStatus(includedCreditUSD, weeklyTokenUsage, s.cfg.AICostUSDPer1KTokens)
	return credits.Remaining > 0, nil
}

func roundCurrency(value float64) float64 {
	return math.Round(value*100) / 100
}

func maxFloat(left float64, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func limitStatus(used int, limit int) (string, string) {
	if limit <= 0 {
		return "unlimited", "unlimited"
	}
	remaining := max(0, limit-used)
	return fmtInt(limit), fmtInt(remaining)
}

func wibLocation() *time.Location {
	return time.FixedZone("WIB", 7*60*60)
}

func nextDailyReset(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
}

func currentFiveHourUsageWindow(now time.Time) (time.Time, time.Time) {
	now = now.In(wibLocation())
	startHour := (now.Hour() / 5) * 5
	start := time.Date(now.Year(), now.Month(), now.Day(), startHour, 0, 0, 0, now.Location())
	reset := start.Add(5 * time.Hour)
	if reset.Day() != start.Day() && reset.Hour() != 0 {
		reset = nextDailyReset(now)
	}
	return start, reset
}

func currentWeeklyUsageWindow(now time.Time) (time.Time, time.Time) {
	now = now.In(wibLocation())
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	startDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(weekday - 1))
	return startDay, startDay.AddDate(0, 0, 7)
}

func formatDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	hours := int(value.Hours())
	minutes := int(value.Minutes()) % 60
	if hours > 0 {
		return fmt.Sprintf("%dh %02dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

func formatCompactNumber(value int) string {
	if value >= 1000000 {
		return fmt.Sprintf("%.1fm", float64(value)/1000000)
	}
	if value >= 1000 {
		return fmt.Sprintf("%.1fk", float64(value)/1000)
	}
	return fmtInt(value)
}

func factualContextPolicy() string {
	return `Factual context policy:
- For questions about the user's Miaw data, especially finance, pangan, assets, health, persona, memories, or saved tracker facts, answer only from the provided Miaw memory/tracker context and the current conversation.
- Do not invent Miaw data, totals, categories, dates, amounts, assets, health logs, or food stock that are not present in context.
- If relevant Miaw tracker data is absent or incomplete, say that the tracker data does not contain enough information yet and ask for the missing input.
- When useful, mention that the answer is based on saved tracker entries.`
}

func systemPromptWithMemory(systemPrompt string, memoryContext string) string {
	systemPrompt = strings.TrimSpace(systemPrompt)
	memoryContext = strings.TrimSpace(memoryContext)
	if memoryContext == "" {
		return systemPrompt
	}
	if systemPrompt == "" {
		return memoryContext
	}
	return systemPrompt + "\n\n" + memoryContext
}

func systemPromptWithContexts(systemPrompt string, contexts ...string) string {
	parts := []string{}
	if trimmed := strings.TrimSpace(systemPrompt); trimmed != "" {
		parts = append(parts, trimmed)
	}
	for _, context := range contexts {
		if trimmed := strings.TrimSpace(context); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n\n")
}

func firstHTTPURL(text string) string {
	for _, field := range strings.Fields(text) {
		candidate := strings.Trim(field, "<>()[]{}\"'.,;:!?")
		parsed, err := url.Parse(candidate)
		if err != nil {
			continue
		}
		if parsed.Scheme == "http" || parsed.Scheme == "https" {
			return parsed.String()
		}
	}
	return ""
}

type webResearchPlan struct {
	NeedsResearch bool
	Query         string
	URL           string
	Reason        string
}

func (s *Server) planWebResearch(ctx context.Context, settings models.RuntimeSettings, message string, force bool, overrideQuery string) webResearchPlan {
	message = strings.TrimSpace(message)
	overrideQuery = strings.TrimSpace(overrideQuery)
	if message == "" {
		return webResearchPlan{Reason: "empty message"}
	}
	if directURL := firstHTTPURL(message); directURL != "" {
		return webResearchPlan{
			NeedsResearch: true,
			URL:           directURL,
			Reason:        "URL detected in user message",
		}
	}
	if force {
		return webResearchPlan{
			NeedsResearch: true,
			Query:         firstNonEmpty(overrideQuery, buildSearchQuery(message)),
			Reason:        "forced by client",
		}
	}
	if shouldForceSearch(message) {
		return webResearchPlan{
			NeedsResearch: true,
			Query:         firstNonEmpty(overrideQuery, buildSearchQuery(message)),
			Reason:        "search keyword detected",
		}
	}
	if !s.cfg.WebResearchEnabled {
		return webResearchPlan{Reason: "web research disabled"}
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	response, err := s.aiClient.Chat(
		ctx,
		ai.RuntimeSettings{
			Provider:     settings.Provider,
			BaseURL:      settings.BaseURL,
			APIKey:       settings.APIKey,
			Model:        settings.Models.Active,
			SystemPrompt: webResearchDecisionPrompt(),
		},
		[]map[string]string{{
			"role":    "user",
			"content": message,
		}},
	)
	if err != nil {
		log.Printf("chat web research planner failed error=%q", err.Error())
		return webResearchPlan{Reason: "AI planner failed"}
	}

	decision := parseWebResearchDecision(response)
	if !decision.NeedsResearch {
		return webResearchPlan{Reason: firstNonEmpty(decision.Reason, "AI planner decided no search")}
	}
	return webResearchPlan{
		NeedsResearch: true,
		Query:         firstNonEmpty(overrideQuery, decision.Query, buildSearchQuery(message)),
		Reason:        firstNonEmpty(decision.Reason, "AI planner requested search"),
	}
}

func webResearchDecisionPrompt() string {
	return `Decide whether the latest user message requires web research before answering.
Return compact JSON only.
Use search when the user asks for current/latest/recent information, news, prices, schedules, changelogs, external verification, URLs, companies/people that may have changed, product availability, or anything likely stale.
Do not search for stable general knowledge, writing help, coding explanation, math, or local app usage.
Format:
{"needs_search":true,"query":"specific search query","reason":"short reason"}
or
{"needs_search":false,"reason":"short reason"}`
}

type webResearchDecision struct {
	NeedsResearch bool   `json:"needs_search"`
	Query         string `json:"query"`
	Reason        string `json:"reason"`
}

func parseWebResearchDecision(raw string) webResearchDecision {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var decision webResearchDecision
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		return webResearchDecision{Reason: "AI planner returned invalid JSON"}
	}
	decision.Query = strings.TrimSpace(decision.Query)
	decision.Reason = strings.TrimSpace(decision.Reason)
	return decision
}

func shouldForceSearch(text string) bool {
	compact := strings.ToLower(strings.Join(strings.Fields(text), " "))
	keywords := []string{
		"cari", "carikan", "search", "googling", "browse", "browsing",
		"berita", "terbaru", "hari ini", "latest", "news", "cek", "update",
		"harga", "jadwal", "rilis", "release", "changelog", "versi terbaru",
	}
	for _, keyword := range keywords {
		if strings.Contains(compact, keyword) {
			return true
		}
	}
	return false
}

func buildSearchQuery(text string) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	replacer := strings.NewReplacer(
		"beritahu", "berita",
		"beri tahu", "berita",
		"kasih tahu", "berita",
		"infoin", "berita",
		"informasikan", "berita",
	)
	normalized = replacer.Replace(normalized)
	stopWords := map[string]bool{
		"cari":     true,
		"carikan":  true,
		"search":   true,
		"googling": true,
		"browse":   true,
		"browsing": true,
		"tolong":   true,
		"dong":     true,
		"please":   true,
	}

	parts := strings.Fields(normalized)
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		token := strings.Trim(strings.ToLower(part), ".,;:!?()[]{}\"'")
		if stopWords[token] {
			continue
		}
		kept = append(kept, part)
	}

	query := strings.Join(kept, " ")
	lowerQuery := strings.ToLower(" " + query + " ")
	if strings.Contains(lowerQuery, " ai ") && (strings.Contains(lowerQuery, " berita ") || strings.Contains(lowerQuery, " terbaru ") || strings.Contains(lowerQuery, " latest ") || strings.Contains(lowerQuery, " news ")) {
		expanded := make([]string, 0, len(kept)+2)
		for _, part := range kept {
			token := strings.Trim(strings.ToLower(part), ".,;:!?()[]{}\"'")
			if token == "ai" {
				expanded = append(expanded, "artificial", "intelligence", "AI")
				continue
			}
			expanded = append(expanded, part)
		}
		query = strings.Join(expanded, " ")
	}
	if needsDateInSearchQuery(query) {
		today := indonesianDate(time.Now().In(time.FixedZone("WIB", 7*60*60)))
		query = strings.TrimSpace(query + " " + today)
	}
	return firstNonEmpty(query, text)
}

func needsDateInSearchQuery(query string) bool {
	query = strings.ToLower(query)
	for _, keyword := range []string{"hari ini", "terbaru", "latest", "news", "berita"} {
		if strings.Contains(query, keyword) {
			return true
		}
	}
	return false
}

func indonesianDate(t time.Time) string {
	months := []string{
		"Januari", "Februari", "Maret", "April", "Mei", "Juni",
		"Juli", "Agustus", "September", "Oktober", "November", "Desember",
	}
	month := months[int(t.Month())-1]
	return fmt.Sprintf("%d %s %d", t.Day(), month, t.Year())
}

func logValue(value string, limit int) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if limit <= 0 || len(normalized) <= limit {
		return normalized
	}
	if limit <= 3 {
		return normalized[:limit]
	}
	return normalized[:limit-3] + "..."
}

func (s *Server) extractMemoriesAsync(userID string, conversationID string, settings models.RuntimeSettings, userMessage string, assistantMessage string) {
	userMessage = strings.TrimSpace(userMessage)
	assistantMessage = strings.TrimSpace(assistantMessage)
	if shouldSkipMemoryExtraction(userMessage, assistantMessage) {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		job, err := s.store.EnqueueMemoryExtractionJob(ctx, userID, conversationID, userMessage, assistantMessage)
		if err != nil {
			log.Printf("memory extraction enqueue failed user=%s conversation=%s error=%q", userID, conversationID, err.Error())
			return
		}
		log.Printf("memory extraction queued user=%s conversation=%s job=%s", userID, conversationID, job.ID)
		s.processDueMemoryExtractionJobs(settings)
	}()
}

func (s *Server) startMemoryExtractionWorker() {
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.processDueMemoryExtractionJobs(models.RuntimeSettings{})
		}
	}()
}

func (s *Server) processDueMemoryExtractionJobs(fallbackSettings models.RuntimeSettings) {
	if !s.memoryWorkerMu.TryLock() {
		return
	}
	defer s.memoryWorkerMu.Unlock()

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		jobs, err := s.store.ClaimDueMemoryExtractionJobs(ctx, 3)
		cancel()
		if err != nil {
			log.Printf("memory extraction claim failed error=%q", err.Error())
			return
		}
		if len(jobs) == 0 {
			return
		}
		for _, job := range jobs {
			s.processMemoryExtractionJob(job, fallbackSettings)
		}
	}
}

func (s *Server) processMemoryExtractionJob(job models.MemoryExtractionJob, fallbackSettings models.RuntimeSettings) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	settings, err := s.loadRuntimeSettings(ctx, job.UserID)
	if err != nil {
		settings = fallbackSettings
	}
	settings = applyRuntimeDefaults(settings)
	if strings.TrimSpace(settings.APIKey) == "" && s.cfg.ManagedAIApiKey != "" {
		settings.APIKey = s.cfg.ManagedAIApiKey
	}
	if strings.TrimSpace(settings.BaseURL) == "" || strings.TrimSpace(settings.Models.Active) == "" || strings.TrimSpace(settings.APIKey) == "" {
		_ = s.store.FailMemoryExtractionJob(ctx, job.ID, "runtime settings are incomplete")
		return
	}

	log.Printf("memory extraction job start job=%s user=%s conversation=%s attempt=%d", job.ID, job.UserID, job.ConversationID, job.Attempts+1)
	response, err := s.aiClient.Chat(
		ctx,
		ai.RuntimeSettings{
			Provider:     settings.Provider,
			BaseURL:      settings.BaseURL,
			APIKey:       settings.APIKey,
			Model:        settings.Models.Active,
			SystemPrompt: memoryExtractionSystemPrompt(),
		},
		[]map[string]string{
			{
				"role": "user",
				"content": fmt.Sprintf(
					"User message:\n%s\n\nAssistant reply:\n%s\n\nReturn JSON only.",
					job.UserMessage,
					job.AssistantReply,
				),
			},
		},
	)
	if err != nil {
		_ = s.store.FailMemoryExtractionJob(ctx, job.ID, err.Error())
		log.Printf("memory extraction job failed job=%s error=%q", job.ID, err.Error())
		return
	}

	count := 0
	for _, input := range parseMemoryExtraction(response) {
		input.SourceConversationID = job.ConversationID
		if input.Metadata == nil {
			input.Metadata = map[string]any{}
		}
		input.Metadata["source"] = "chat_extraction"
		input.Metadata["jobId"] = job.ID
		if _, err := s.store.UpsertMemory(ctx, job.UserID, input); err != nil {
			_ = s.store.FailMemoryExtractionJob(ctx, job.ID, err.Error())
			log.Printf("memory extraction job save failed job=%s error=%q", job.ID, err.Error())
			return
		}
		count++
	}
	_ = s.store.CompleteMemoryExtractionJob(ctx, job.ID)
	log.Printf("memory extraction job done job=%s memories=%d", job.ID, count)
}

func shouldSkipMemoryExtraction(userMessage string, assistantMessage string) bool {
	compact := strings.ToLower(strings.Join(strings.Fields(userMessage), " "))
	if len(compact) < 24 {
		switch compact {
		case "ok", "oke", "ya", "yes", "thanks", "thank you", "makasih", "terima kasih", "lanjut":
			return true
		}
	}
	return len(userMessage)+len(assistantMessage) < 80
}

func memoryExtractionSystemPrompt() string {
	return `Extract durable user memory from the latest chat turn.
Only save stable facts useful across Miaw modules: user preferences, recurring goals, finance/pangan/assets context, project constraints, identity details, or explicit long-term instructions.
Do not save one-off questions, temporary wording, secrets, passwords, API keys, payment credentials, or sensitive data that is not clearly needed.
Return compact JSON only:
{"memories":[{"domain":"chat|finance|pangan|assets|report|general","kind":"preference|fact|goal|instruction|profile","title":"short title","content":"one clear sentence","confidence":0.0}]}
If nothing should be remembered, return {"memories":[]}.`
}

func parseMemoryExtraction(raw string) []models.MemoryInput {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var payload struct {
		Memories []struct {
			Domain     string  `json:"domain"`
			Kind       string  `json:"kind"`
			Title      string  `json:"title"`
			Content    string  `json:"content"`
			Confidence float64 `json:"confidence"`
		} `json:"memories"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}

	inputs := make([]models.MemoryInput, 0, len(payload.Memories))
	for _, item := range payload.Memories {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		inputs = append(inputs, models.MemoryInput{
			Domain:     item.Domain,
			Kind:       item.Kind,
			Title:      item.Title,
			Content:    content,
			Confidence: item.Confidence,
		})
	}
	if len(inputs) > 5 {
		return inputs[:5]
	}
	return inputs
}

func (s *Server) extractTrackersAsync(userID string, conversationID string, messageID string, settings models.RuntimeSettings, userMessage string, images []savedChatImage) {
	if shouldSkipTrackerExtraction(userMessage, images) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		parts := []map[string]any{
			{
				"type": "text",
				"text": strings.TrimSpace(userMessage + "\n\nExtract tracker entries from the attached image(s). Return JSON only."),
			},
		}
		for _, image := range images {
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": image.DataURL,
				},
			})
		}

		response, err := s.aiClient.ChatAny(
			ctx,
			ai.RuntimeSettings{
				Provider:     settings.Provider,
				BaseURL:      settings.BaseURL,
				APIKey:       settings.APIKey,
				Model:        settings.Models.Active,
				SystemPrompt: trackerExtractionSystemPrompt(),
			},
			[]map[string]any{{"role": "user", "content": parts}},
		)
		if err != nil {
			return
		}

		for _, input := range parseTrackerExtraction(response) {
			if input.Metadata == nil {
				input.Metadata = map[string]any{}
			}
			input.Source = "Image Upload"
			input.UpdatedFrom = "Miaw AI image extraction"
			input.Metadata["source"] = "image_extraction"
			if len(images) == 0 {
				input.Source = "Miaw AI Chat"
				input.UpdatedFrom = "Miaw AI text extraction"
				input.Metadata["source"] = "chat_extraction"
			}
			input.Metadata["conversationId"] = conversationID
			input.Metadata["messageId"] = messageID
			input.Metadata["imageUrls"] = publicURLs(images)
			_, _ = s.store.CreateTrackerSuggestion(ctx, userID, input)
		}
	}()
}

func shouldSkipTrackerExtraction(userMessage string, images []savedChatImage) bool {
	if len(images) > 0 {
		return false
	}
	compact := strings.ToLower(strings.Join(strings.Fields(userMessage), " "))
	if len(compact) < 12 {
		return true
	}
	switch compact {
	case "ok", "oke", "ya", "yes", "thanks", "thank you", "makasih", "terima kasih", "lanjut":
		return true
	}
	return false
}

func trackerExtractionSystemPrompt() string {
	return `You extract structured Miaw ecosystem tracker entries from user text and attached images.
Supported modules:
- finance: income/outcome, receipts, bills, balances, salary, expenses.
- pangan: groceries, staple needs, food stock, daily primary needs.
- assets: owned items, age, condition, function, resale or maintenance value.
- health: exercise, sleep, weight, medication, wellness logs.
- persona: durable summary about user preferences, habits, goals, or patterns.

Rules:
- Return compact JSON only, no markdown.
- Create entries only when the image/text gives useful user data.
- A receipt can create both finance and pangan entries.
- Use Indonesian-friendly labels when the input is Indonesian.
- Do not invent amounts or details that are not visible.
- If nothing useful is found, return {"entries":[]}.

Schema:
{"entries":[{"module":"finance|pangan|assets|health|persona","title":"short title","amount":"optional amount/qty/value","status":"Income|Outcome|Tracked|Need review|Layak jual|Tidak layak jual|Selesai|Aktif","category":"short category","detail":"clear extracted detail","metadata":{"date":"YYYY-MM-DD if known","currency":"IDR if known","amountNumber":12000,"quantity":2,"unit":"kg","condition":"good|fair|poor","confidence":0.0}}]}`
}

func parseTrackerExtraction(raw string) []models.TrackerEntryInput {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var payload struct {
		Entries []models.TrackerEntryInput `json:"entries"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}

	inputs := make([]models.TrackerEntryInput, 0, len(payload.Entries))
	for _, entry := range payload.Entries {
		if strings.TrimSpace(entry.Title) == "" {
			continue
		}
		if err := validateTrackerInput(entry); err != nil {
			continue
		}
		inputs = append(inputs, entry)
	}
	if len(inputs) > 12 {
		return inputs[:12]
	}
	return inputs
}

func normalizeMidtransStatus(transactionStatus string, fraudStatus string) string {
	status := strings.ToLower(strings.TrimSpace(transactionStatus))
	fraud := strings.ToLower(strings.TrimSpace(fraudStatus))
	if status == "capture" && fraud == "challenge" {
		return "challenge"
	}
	return status
}

func subscriptionPeriodEnd(productID string) time.Time {
	now := time.Now().UTC()
	switch productID {
	case "miaw_pro_yearly_idr_590000":
		return now.AddDate(1, 0, 0)
	default:
		return now.AddDate(0, 1, 0)
	}
}

func checkoutProductAmount(productID string) (int, bool) {
	switch productID {
	case "miaw_pro_monthly_idr_59000":
		return 59000, true
	case "miaw_pro_yearly_idr_590000":
		return 590000, true
	case "miaw_credit_usd_5_idr_25000":
		return 25000, true
	case "miaw_credit_usd_10_idr_50000":
		return 50000, true
	case "miaw_credit_usd_20_idr_100000":
		return 100000, true
	default:
		return 0, false
	}
}

func isSubscriptionProduct(productID string) bool {
	switch productID {
	case "miaw_pro_monthly_idr_59000", "miaw_pro_yearly_idr_590000":
		return true
	default:
		return false
	}
}
