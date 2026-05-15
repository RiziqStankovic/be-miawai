package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Port                      string
	DatabaseURL               string
	AppBaseURL                string
	APIBaseURL                string
	CORSOrigins               []string
	EnableDevAuth             bool
	DefaultProvider           string
	DefaultProviderBaseURL    string
	DefaultProviderAPIKey     string
	DefaultProviderModels     []string
	DefaultProviderSystem     string
	SessionSecret             string
	CookieSecure              bool
	GoogleClientID            string
	GoogleClientSecret        string
	GitHubClientID            string
	GitHubClientSecret        string
	SubscriptionWebhookSecret string

	// Subscription & Payments
	MidtransServerKey            string
	MidtransClientKey            string
	MidtransBaseURL              string
	MidtransSnapBaseURL          string
	MidtransIsProduction         bool
	GooglePlayServiceAccountJSON string
	GooglePlayPackageName        string

	// Quotas & AI Costs
	FreeUserDailyPromptLimit      int
	ProUserDailyPromptLimit       int
	FreeUserDailyImageLimit       int
	ProUserDailyImageLimit        int
	FreeUserDailyWebResearchLimit int
	ProUserDailyWebResearchLimit  int
	FreeUserFiveHourTokenLimit    int
	ProUserFiveHourTokenLimit     int
	FreeUserWeeklyTokenLimit      int
	ProUserWeeklyTokenLimit       int
	FreeUserIncludedCreditUSD     float64
	ProUserIncludedCreditUSD      float64
	AICostUSDPer1KTokens          float64
	ManagedAIApiKey               string
	ChatContextMessageLimit       int
	ChatContextCharLimit          int
	MaxPromptChars                int
	MinChatIntervalMs             int

	// Web research
	WebResearchEnabled         bool
	SearxngURL                 string
	WebResearchTimeoutMs       int
	WebResearchMaxResults      int
	WebResearchTargetPages     int
	WebResearchMaxContentChars int

	// WhatsApp channel adapter
	WhatsAppInternalToken string
	WhatsAppEnabled       bool
	WhatsAppOwnerUserID   string
	WhatsAppListenGroups  bool
	WhatsAppSessionDB     string

	AdminBootstrapEnabled  bool
	AdminBootstrapEmail    string
	AdminBootstrapName     string
	AdminBootstrapPassword string
}

func Load() Config {
	loadDotEnvFiles()

	return Config{
		Port:                      getenv("PORT", "8080"),
		DatabaseURL:               getenv("DATABASE_URL", "postgres://miaw:miaw@localhost:5432/miawai?sslmode=disable"),
		AppBaseURL:                strings.TrimRight(getenv("APP_BASE_URL", "http://localhost:5173"), "/"),
		APIBaseURL:                strings.TrimRight(getenv("API_BASE_URL", "http://localhost:8080"), "/"),
		CORSOrigins:               splitCSV(getenv("CORS_ORIGINS", "http://localhost:5173")),
		EnableDevAuth:             getenv("DEV_AUTH_ENABLED", "true") == "true",
		DefaultProvider:           getenv("THUKI_PROVIDER", "openai"),
		DefaultProviderBaseURL:    getenv("THUKI_API_BASE_URL", ""),
		DefaultProviderAPIKey:     strings.TrimSpace(os.Getenv("THUKI_API_KEY")),
		DefaultProviderModels:     splitCSV(getenv("THUKI_SUPPORTED_AI_MODELS", "gpt-4o-mini")),
		DefaultProviderSystem:     strings.TrimSpace(os.Getenv("THUKI_SYSTEM_PROMPT")),
		SessionSecret:             getenv("SESSION_SECRET", "dev-only-change-me"),
		CookieSecure:              getenv("COOKIE_SECURE", "false") == "true",
		GoogleClientID:            os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret:        os.Getenv("GOOGLE_CLIENT_SECRET"),
		GitHubClientID:            os.Getenv("GITHUB_CLIENT_ID"),
		GitHubClientSecret:        os.Getenv("GITHUB_CLIENT_SECRET"),
		SubscriptionWebhookSecret: getenv("SUBSCRIPTION_WEBHOOK_SECRET", "dev-webhook-secret"),

		MidtransServerKey:            firstNonEmpty(os.Getenv("MIDTRANS_SERVER_KEY"), os.Getenv("midtrans_server_key")),
		MidtransClientKey:            firstNonEmpty(os.Getenv("MIDTRANS_CLIENT_KEY"), os.Getenv("midtrans_client_key")),
		MidtransBaseURL:              strings.TrimRight(firstNonEmpty(os.Getenv("MIDTRANS_BASE_URL"), os.Getenv("midtrans_base_url")), "/"),
		MidtransSnapBaseURL:          strings.TrimRight(firstNonEmpty(os.Getenv("MIDTRANS_SNAP_BASE_URL"), os.Getenv("midtrans_snap_base_url")), "/"),
		MidtransIsProduction:         getenv("MIDTRANS_IS_PRODUCTION", "false") == "true",
		GooglePlayServiceAccountJSON: os.Getenv("GOOGLE_PLAY_SERVICE_ACCOUNT_JSON"),
		GooglePlayPackageName:        getenv("GOOGLE_PLAY_PACKAGE_NAME", "id.cloudfren.miaw.dynamicisland"),

		FreeUserDailyPromptLimit:      parseInt(getenv("FREE_USER_DAILY_PROMPT_LIMIT", "20"), 20),
		ProUserDailyPromptLimit:       parseInt(getenv("PRO_USER_DAILY_PROMPT_LIMIT", "0"), 0),
		FreeUserDailyImageLimit:       parseInt(getenv("FREE_USER_DAILY_IMAGE_LIMIT", "5"), 5),
		ProUserDailyImageLimit:        parseInt(getenv("PRO_USER_DAILY_IMAGE_LIMIT", "0"), 0),
		FreeUserDailyWebResearchLimit: parseInt(getenv("FREE_USER_DAILY_WEB_RESEARCH_LIMIT", "0"), 0),
		ProUserDailyWebResearchLimit:  parseInt(getenv("PRO_USER_DAILY_WEB_RESEARCH_LIMIT", "0"), 0),
		FreeUserFiveHourTokenLimit:    parseInt(getenv("FREE_USER_5H_TOKEN_LIMIT", "50000"), 50000),
		ProUserFiveHourTokenLimit:     parseInt(getenv("PRO_USER_5H_TOKEN_LIMIT", "500000"), 500000),
		FreeUserWeeklyTokenLimit:      parseInt(getenv("FREE_USER_WEEKLY_TOKEN_LIMIT", "200000"), 200000),
		ProUserWeeklyTokenLimit:       parseInt(getenv("PRO_USER_WEEKLY_TOKEN_LIMIT", "2000000"), 2000000),
		FreeUserIncludedCreditUSD:     parseFloat(getenv("FREE_USER_INCLUDED_CREDIT_USD", "0.5"), 0.5),
		ProUserIncludedCreditUSD:      parseFloat(getenv("PRO_USER_INCLUDED_CREDIT_USD", "5"), 5),
		AICostUSDPer1KTokens:          parseFloat(getenv("AI_COST_USD_PER_1K_TOKENS", "0.002"), 0.002),
		ManagedAIApiKey:               os.Getenv("MANAGED_AI_API_KEY"),
		ChatContextMessageLimit:       parseInt(getenv("CHAT_CONTEXT_MESSAGE_LIMIT", "24"), 24),
		ChatContextCharLimit:          parseInt(getenv("CHAT_CONTEXT_CHAR_LIMIT", "24000"), 24000),
		MaxPromptChars:                parseInt(getenv("MAX_PROMPT_CHARS", "12000"), 12000),
		MinChatIntervalMs:             parseInt(getenv("MIN_CHAT_INTERVAL_MS", "1200"), 1200),

		WebResearchEnabled:         getenv("WEB_RESEARCH_ENABLED", "false") == "true",
		SearxngURL:                 strings.TrimRight(getenv("SEARXNG_URL", "http://127.0.0.1:25017"), "/"),
		WebResearchTimeoutMs:       parseInt(getenv("WEB_RESEARCH_TIMEOUT_MS", "12000"), 12000),
		WebResearchMaxResults:      parseInt(getenv("WEB_RESEARCH_MAX_RESULTS", "5"), 5),
		WebResearchTargetPages:     parseInt(getenv("WEB_RESEARCH_TARGET_PAGES", "2"), 2),
		WebResearchMaxContentChars: parseInt(getenv("WEB_RESEARCH_MAX_CONTENT_CHARS", "6000"), 6000),

		WhatsAppInternalToken: os.Getenv("WHATSAPP_INTERNAL_TOKEN"),
		WhatsAppEnabled:       getenv("WHATSAPP_ENABLED", "false") == "true",
		WhatsAppOwnerUserID:   os.Getenv("WHATSAPP_OWNER_USER_ID"),
		WhatsAppListenGroups:  getenv("WHATSAPP_LISTEN_GROUPS", "false") == "true",
		WhatsAppSessionDB:     getenv("WHATSAPP_SESSION_DB", "data/whatsapp.db"),

		AdminBootstrapEnabled:  getenv("ADMIN_BOOTSTRAP_ENABLED", "true") == "true",
		AdminBootstrapEmail:    getenv("ADMIN_BOOTSTRAP_EMAIL", "admin@miaw.local"),
		AdminBootstrapName:     getenv("ADMIN_BOOTSTRAP_NAME", "Miaw Admin"),
		AdminBootstrapPassword: getenv("ADMIN_BOOTSTRAP_PASSWORD", "admin123"),
	}
}

func loadDotEnvFiles() {
	seen := map[string]bool{}
	for _, path := range dotenvCandidates() {
		cleaned, err := filepath.Abs(path)
		if err != nil {
			cleaned = filepath.Clean(path)
		}
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		loadDotEnv(cleaned)
	}
}

func dotenvCandidates() []string {
	candidates := []string{".env"}

	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".env"))
	}

	if wd, err := os.Getwd(); err == nil {
		for dir := wd; ; dir = filepath.Dir(dir) {
			candidates = append(candidates,
				filepath.Join(dir, ".env"),
				filepath.Join(dir, "be-miawai", ".env"),
			)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
	}

	return candidates
}

func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			key, value, ok = strings.Cut(line, ":")
		}
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		os.Setenv(key, strings.Trim(strings.TrimSpace(value), `"`))
	}
}

func getenv(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimRight(strings.TrimSpace(part), "/"); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseInt(value string, fallback int) int {
	var result int
	for _, c := range value {
		if c >= '0' && c <= '9' {
			result = result*10 + int(c-'0')
		} else {
			return fallback
		}
	}
	return result
}

func parseFloat(value string, fallback float64) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return fallback
	}
	return parsed
}
