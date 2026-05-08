# be-miawai

Go API for Miaw AI web, desktop, and Android entitlement handling.

## Included

- Postgres schema for users, OAuth accounts, subscriptions, and chat messages.
- Google and GitHub OAuth login with signed HTTP-only session cookies.
- Authenticated profile endpoint used by the React web app.
- Subscription upsert endpoint for signed-in clients and signed webhook endpoint for server-to-server updates.
- Runtime settings persistence for provider, base URL, API key, model catalog, and system prompt.
- Conversation history with pinned threads, saved messages, and streaming chat responses through an OpenAI-compatible API.

## Run locally

```powershell
docker compose up -d
go mod tidy
go run ./cmd/api
```

The API auto-runs every `.sql` file in `migrations/` during startup, so tables are created on first boot.

Copy `.env.example` to `.env` or export the same variables before running. The default runtime settings can reuse the same `THUKI_*` variables used by `miaw-ai-chat-windows`.

For OAuth callbacks, configure:

- Google callback: `http://localhost:8080/v1/auth/google/callback`
- GitHub callback: `http://localhost:8080/v1/auth/github/callback`

## Webhook signature

`POST /v1/webhooks/subscriptions` expects `X-Miaw-Signature` as a hex HMAC-SHA256 of the raw JSON body using `SUBSCRIPTION_WEBHOOK_SECRET`.
