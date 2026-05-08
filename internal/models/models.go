package models

import "time"

type User struct {
	ID                 string     `json:"id"`
	Email              string     `json:"email"`
	Name               string     `json:"name"`
	AvatarURL          string     `json:"avatarUrl"`
	Plan               string     `json:"plan"`
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
	Provider           string    `json:"provider"`
	Model              string    `json:"model"`
	LastMessagePreview string    `json:"lastMessagePreview"`
	MessageCount       int       `json:"messageCount"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type ConversationMessage struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversationId"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	CreatedAt      time.Time `json:"createdAt"`
}
