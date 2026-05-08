package config

import (
	"bufio"
	"os"
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
}

func Load() Config {
	loadDotEnv(".env")

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
	}
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

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
