# be-miawai

Go API for Miaw AI web, desktop, and Android entitlement handling.

## Included

- Postgres schema for users, OAuth accounts, subscriptions, and chat messages.
- Google and GitHub OAuth login with signed HTTP-only session cookies.
- Authenticated profile endpoint used by the React web app.
- Subscription upsert endpoint for signed-in clients and signed webhook endpoint for server-to-server updates.
- Runtime settings persistence for provider, base URL, API key, model catalog, and system prompt.
- Conversation history with pinned threads, saved messages, and streaming chat responses through an OpenAI-compatible API.
- Optional web research through SearXNG plus native Go HTTP page reading, without Playwright or Lightpanda.
- Embedded WhatsApp channel for the central Miaw AI number, including QR status and link-code verification.

## Run locally

```powershell
docker compose up -d
go mod tidy
go run ./cmd/api
```

The API auto-runs every `.sql` file in `migrations/` during startup, so tables are created on first boot.

Copy `.env.example` to `.env` or export the same variables before running. The default runtime settings can reuse the same `THUKI_*` variables used by `miaw-ai-chat-windows`.

## Web research

Set `WEB_RESEARCH_ENABLED=true` and run the `searxng` compose service to enable authenticated research endpoints:

- `POST /v1/research/search` with `{ "query": "..." }`
- `POST /v1/research/read-url` with `{ "url": "https://..." }`

`POST /v1/chat/stream` also accepts `"web": true`. If the message contains an `http` or `https` URL, the backend reads that URL; otherwise it searches SearXNG and injects the fetched context into the model prompt.

For OAuth callbacks, set `API_BASE_URL` to the public backend origin used by
Google/GitHub, then configure matching provider callbacks:

- Google callback: `${API_BASE_URL}/v1/auth/google/callback`
- GitHub callback: `${API_BASE_URL}/v1/auth/github/callback`

For production, `APP_BASE_URL`, `API_BASE_URL`, `CORS_ORIGINS`, and provider
callback URLs must use the same deployed frontend/backend origins. If the API is
served over HTTPS, also set `COOKIE_SECURE=true`.

## Webhook signature

`POST /v1/webhooks/subscriptions` expects `X-Miaw-Signature` as a hex HMAC-SHA256 of the raw JSON body using `SUBSCRIPTION_WEBHOOK_SECRET`.

## WhatsApp

Embedded WhatsApp can run inside the API process:

```powershell
$env:WHATSAPP_ENABLED="true"
$env:WHATSAPP_OWNER_USER_ID="usr_admin_..."
$env:WHATSAPP_LISTEN_GROUPS="false"
$env:WHATSAPP_SESSION_DB="data/whatsapp.db"
go run ./cmd/api
```

On startup the API creates or reuses one `central_bot` WhatsApp account owned by
`WHATSAPP_OWNER_USER_ID`, stores the QR/status in `whatsapp_accounts`, and routes
incoming messages directly into the backend chat pipeline. Unknown contacts must
verify with a link code generated from Miaw web before they can chat with AI.

For the current MVP, use the central Miaw number flow only. Users generate a
link code from Settings > WhatsApp, then send that code to the central Miaw
WhatsApp number. Codes expire after 10 minutes and lock after 3 wrong attempts.
After verification, messages from that WhatsApp contact are routed to the linked
Miaw user.
