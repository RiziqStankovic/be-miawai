package models

import (
	"time"

	"be-miawai/internal/research"
)

type User struct {
	ID                 string     `json:"id"`
	Email              string     `json:"email"`
	Name               string     `json:"name"`
	AvatarURL          string     `json:"avatarUrl"`
	Plan               string     `json:"plan"`
	Role               string     `json:"role"`
	Access             []string   `json:"access"`
	SubscriptionStatus string     `json:"subscriptionStatus"`
	EntitledUntil      *time.Time `json:"entitledUntil"`
	CreatedAt          time.Time  `json:"createdAt"`
}

type OAuthProfile struct {
	Provider       string
	ProviderUserID string
	Email          string
	Name           string
	AvatarURL      string
}

type ChatMessage struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

type Subscription struct {
	ID               string     `json:"id"`
	UserID           string     `json:"userId"`
	Platform         string     `json:"platform"`
	ProductID        string     `json:"productId"`
	Status           string     `json:"status"`
	CurrentPeriodEnd *time.Time `json:"currentPeriodEnd"`
}

type PaymentTransaction struct {
	ID         string                 `json:"id"`
	UserID     string                 `json:"userId"`
	OrderID    string                 `json:"orderId"`
	Platform   string                 `json:"platform"`
	ProductID  string                 `json:"productId"`
	Amount     int                    `json:"amount"`
	Currency   string                 `json:"currency"`
	Status     string                 `json:"status"`
	RawPayload map[string]interface{} `json:"rawPayload"`
	CreatedAt  time.Time              `json:"createdAt"`
	UpdatedAt  time.Time              `json:"updatedAt"`
}

type UserDailyUsage struct {
	UserID        string    `json:"userId"`
	UsageDate     time.Time `json:"usageDate"`
	PromptCount   int       `json:"promptCount"`
	ImageCount    int       `json:"imageCount"`
	ResearchCount int       `json:"researchCount"`
	TokenInput    int       `json:"tokenInput"`
	TokenOutput   int       `json:"tokenOutput"`
}

type UsageWindow struct {
	PromptCount   int `json:"promptCount"`
	ImageCount    int `json:"imageCount"`
	ResearchCount int `json:"researchCount"`
	TokenInput    int `json:"tokenInput"`
	TokenOutput   int `json:"tokenOutput"`
}

type RuntimeModels struct {
	Active string   `json:"active"`
	All    []string `json:"all"`
}

type RuntimeSettings struct {
	Provider     string        `json:"provider"`
	BaseURL      string        `json:"baseUrl"`
	APIKey       string        `json:"apiKey"`
	SystemPrompt string        `json:"systemPrompt"`
	Models       RuntimeModels `json:"models"`
}

type Conversation struct {
	ID                 string    `json:"id"`
	Title              string    `json:"title"`
	Pinned             bool      `json:"pinned"`
	IsCloudSynced      bool      `json:"isCloudSynced"`
	Channel            string    `json:"channel"`
	ChannelThreadID    string    `json:"channelThreadId,omitempty"`
	ChannelDisplayName string    `json:"channelDisplayName,omitempty"`
	Provider           string    `json:"provider"`
	Model              string    `json:"model"`
	LastMessagePreview string    `json:"lastMessagePreview"`
	MessageCount       int       `json:"messageCount"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type WhatsAppAccount struct {
	ID           string    `json:"id"`
	OwnerUserID  string    `json:"ownerUserId"`
	Mode         string    `json:"mode"`
	DisplayName  string    `json:"displayName"`
	PhoneJID     string    `json:"phoneJid"`
	Status       string    `json:"status"`
	SessionRef   string    `json:"sessionRef"`
	AccessPolicy string    `json:"accessPolicy"`
	QRCode       string    `json:"qrCode,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type WhatsAppContact struct {
	ID                    string    `json:"id"`
	WhatsAppAccountID     string    `json:"whatsappAccountId"`
	OwnerUserID           string    `json:"ownerUserId"`
	ContactJID            string    `json:"contactJid"`
	DisplayName           string    `json:"displayName"`
	LinkedUserID          string    `json:"linkedUserId,omitempty"`
	DefaultConversationID string    `json:"defaultConversationId,omitempty"`
	AllowStatus           string    `json:"allowStatus"`
	VerificationAttempts  int       `json:"verificationAttempts"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

type WhatsAppLinkCode struct {
	ID                string    `json:"id"`
	UserID            string    `json:"userId"`
	PhoneNumber       string    `json:"phoneNumber"`
	Code              string    `json:"code"`
	Status            string    `json:"status"`
	Attempts          int       `json:"attempts"`
	ExpiresAt         time.Time `json:"expiresAt"`
	MatchedContactJID string    `json:"matchedContactJid"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type WhatsAppEvent struct {
	ID                string    `json:"id"`
	WhatsAppAccountID string    `json:"whatsappAccountId"`
	ContactJID        string    `json:"contactJid"`
	SenderJID         string    `json:"senderJid"`
	Direction         string    `json:"direction"`
	MessageID         string    `json:"messageId"`
	ConversationID    string    `json:"conversationId,omitempty"`
	Text              string    `json:"text"`
	CreatedAt         time.Time `json:"createdAt"`
}

type ConversationMessage struct {
	ID             string           `json:"id"`
	ConversationID string           `json:"conversationId"`
	Role           string           `json:"role"`
	Content        string           `json:"content"`
	ImageURLs      []string         `json:"imageUrls,omitempty"`
	ResearchReport *research.Report `json:"researchReport,omitempty"`
	CreatedAt      time.Time        `json:"createdAt"`
}

type ChatImageInput struct {
	Name       string `json:"name"`
	MimeType   string `json:"mimeType"`
	DataBase64 string `json:"dataBase64"`
}

type UserMemory struct {
	ID                   string         `json:"id"`
	Domain               string         `json:"domain"`
	Kind                 string         `json:"kind"`
	Title                string         `json:"title"`
	Content              string         `json:"content"`
	SourceConversationID string         `json:"sourceConversationId,omitempty"`
	Confidence           float64        `json:"confidence"`
	Metadata             map[string]any `json:"metadata"`
	CreatedAt            time.Time      `json:"createdAt"`
	UpdatedAt            time.Time      `json:"updatedAt"`
}

type MemoryExtractionJob struct {
	ID             string    `json:"id"`
	UserID         string    `json:"userId"`
	ConversationID string    `json:"conversationId"`
	UserMessage    string    `json:"userMessage"`
	AssistantReply string    `json:"assistantReply"`
	Status         string    `json:"status"`
	Attempts       int       `json:"attempts"`
	LastError      string    `json:"lastError,omitempty"`
	RunAfter       time.Time `json:"runAfter"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type MemoryInput struct {
	Domain               string         `json:"domain"`
	Kind                 string         `json:"kind"`
	Title                string         `json:"title"`
	Content              string         `json:"content"`
	SourceConversationID string         `json:"sourceConversationId"`
	Confidence           float64        `json:"confidence"`
	Metadata             map[string]any `json:"metadata"`
}

type TrackerEntry struct {
	ID          string         `json:"id"`
	Module      string         `json:"module"`
	Title       string         `json:"title"`
	Amount      string         `json:"amount"`
	Status      string         `json:"status"`
	Category    string         `json:"category"`
	Detail      string         `json:"detail"`
	Source      string         `json:"source"`
	UpdatedFrom string         `json:"updatedFrom"`
	Metadata    map[string]any `json:"metadata"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

type TrackerEntryInput struct {
	Module      string         `json:"module"`
	Title       string         `json:"title"`
	Amount      string         `json:"amount"`
	Status      string         `json:"status"`
	Category    string         `json:"category"`
	Detail      string         `json:"detail"`
	Source      string         `json:"source"`
	UpdatedFrom string         `json:"updatedFrom"`
	Metadata    map[string]any `json:"metadata"`
}

type TrackerSuggestion struct {
	ID                    string         `json:"id"`
	Module                string         `json:"module"`
	Title                 string         `json:"title"`
	Amount                string         `json:"amount"`
	Status                string         `json:"status"`
	Category              string         `json:"category"`
	Detail                string         `json:"detail"`
	Source                string         `json:"source"`
	UpdatedFrom           string         `json:"updatedFrom"`
	Metadata              map[string]any `json:"metadata"`
	ReviewStatus          string         `json:"reviewStatus"`
	CreatedTrackerEntryID string         `json:"createdTrackerEntryId,omitempty"`
	CreatedAt             time.Time      `json:"createdAt"`
	UpdatedAt             time.Time      `json:"updatedAt"`
}
