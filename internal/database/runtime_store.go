package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"be-miawai/internal/models"
)

func (s *Store) GetRuntimeSettings(ctx context.Context, userID string) (models.RuntimeSettings, error) {
	var settings models.RuntimeSettings
	var apiKey string
	var rawModels []byte

	err := s.db.QueryRowContext(
		ctx,
		`SELECT provider, base_url, api_key, active_model, model_catalog, system_prompt
		 FROM user_runtime_settings
		 WHERE user_id = $1`,
		userID,
	).Scan(
		&settings.Provider,
		&settings.BaseURL,
		&apiKey,
		&settings.Models.Active,
		&rawModels,
		&settings.SystemPrompt,
	)
	if err != nil {
		return models.RuntimeSettings{}, err
	}

	settings.APIKey = apiKey
	if err := json.Unmarshal(rawModels, &settings.Models.All); err != nil {
		return models.RuntimeSettings{}, err
	}

	return settings, nil
}

func (s *Store) UpsertRuntimeSettings(ctx context.Context, userID string, settings models.RuntimeSettings) (models.RuntimeSettings, error) {
	settings.Provider = strings.TrimSpace(settings.Provider)
	settings.BaseURL = strings.TrimSpace(settings.BaseURL)
	settings.APIKey = strings.TrimSpace(settings.APIKey)
	settings.SystemPrompt = strings.TrimSpace(settings.SystemPrompt)
	settings.Models.Active = strings.TrimSpace(settings.Models.Active)
	settings.Models.All = normalizeModelList(settings.Models.Active, settings.Models.All)
	if settings.Provider == "" {
		settings.Provider = "openai"
	}

	rawModels, err := json.Marshal(settings.Models.All)
	if err != nil {
		return models.RuntimeSettings{}, err
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO user_runtime_settings (
		   user_id, provider, base_url, api_key, active_model, model_catalog, system_prompt
		 )
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (user_id)
		 DO UPDATE SET provider = EXCLUDED.provider,
		               base_url = EXCLUDED.base_url,
		               api_key = EXCLUDED.api_key,
		               active_model = EXCLUDED.active_model,
		               model_catalog = EXCLUDED.model_catalog,
		               system_prompt = EXCLUDED.system_prompt,
		               updated_at = NOW()`,
		userID,
		settings.Provider,
		settings.BaseURL,
		settings.APIKey,
		settings.Models.Active,
		rawModels,
		settings.SystemPrompt,
	)
	if err != nil {
		return models.RuntimeSettings{}, err
	}

	return settings, nil
}

func (s *Store) ListConversationsByUser(ctx context.Context, userID string) ([]models.Conversation, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, title, pinned, is_cloud_synced, channel, channel_thread_id, channel_display_name, provider, model, last_message_preview, message_count, updated_at
		 FROM conversations
		 WHERE user_id = $1 AND is_cloud_synced = true
		 ORDER BY pinned DESC, updated_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	conversations := make([]models.Conversation, 0)
	for rows.Next() {
		conversation, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		conversations = append(conversations, conversation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return conversations, nil
}

func (s *Store) ListWhatsAppConversations(ctx context.Context, limit int) ([]models.Conversation, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, title, pinned, is_cloud_synced, channel, channel_thread_id, channel_display_name, provider, model, last_message_preview, message_count, updated_at
		 FROM conversations
		 WHERE channel = 'whatsapp' AND is_cloud_synced = true
		 ORDER BY updated_at DESC
		 LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	conversations := make([]models.Conversation, 0)
	for rows.Next() {
		conversation, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		conversations = append(conversations, conversation)
	}
	return conversations, rows.Err()
}

func (s *Store) GetWhatsAppConversationByID(ctx context.Context, conversationID string) (models.Conversation, error) {
	return scanConversation(s.db.QueryRowContext(
		ctx,
		`SELECT id, title, pinned, is_cloud_synced, channel, channel_thread_id, channel_display_name, provider, model, last_message_preview, message_count, updated_at
		 FROM conversations
		 WHERE id = $1 AND channel = 'whatsapp'`,
		conversationID,
	))
}

func (s *Store) CreateConversation(ctx context.Context, userID string, title string, provider string, model string) (models.Conversation, error) {
	now := time.Now().UTC()
	conversation := models.Conversation{
		ID:                 newID("con"),
		Title:              fallbackTitle(title),
		Provider:           strings.TrimSpace(provider),
		Model:              strings.TrimSpace(model),
		LastMessagePreview: "",
		MessageCount:       0,
		UpdatedAt:          now,
	}

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO conversations (
		   id, user_id, title, provider, model, last_message_preview, message_count, created_at, updated_at, is_cloud_synced, channel
		 )
		 VALUES ($1, $2, $3, $4, $5, '', 0, $6, $6, true, 'web')`,
		conversation.ID,
		userID,
		conversation.Title,
		conversation.Provider,
		conversation.Model,
		now,
	)
	if err != nil {
		return models.Conversation{}, err
	}

	return conversation, nil
}

func (s *Store) GetConversationByID(ctx context.Context, userID string, conversationID string) (models.Conversation, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, title, pinned, is_cloud_synced, channel, channel_thread_id, channel_display_name, provider, model, last_message_preview, message_count, updated_at
		 FROM conversations
		 WHERE id = $1 AND user_id = $2`,
		conversationID,
		userID,
	)
	return scanConversation(row)
}

func (s *Store) UpdateConversationMeta(ctx context.Context, userID string, conversationID string, title *string, pinned *bool, isCloudSynced *bool) (models.Conversation, error) {
	current, err := s.GetConversationByID(ctx, userID, conversationID)
	if err != nil {
		return models.Conversation{}, err
	}

	nextTitle := current.Title
	nextPinned := current.Pinned
	nextSynced := current.IsCloudSynced
	if title != nil {
		nextTitle = fallbackTitle(strings.TrimSpace(*title))
	}
	if pinned != nil {
		nextPinned = *pinned
	}
	if isCloudSynced != nil {
		nextSynced = *isCloudSynced
	}

	_, err = s.db.ExecContext(
		ctx,
		`UPDATE conversations
		 SET title = $3, pinned = $4, is_cloud_synced = $5, updated_at = NOW()
		 WHERE id = $1 AND user_id = $2`,
		conversationID,
		userID,
		nextTitle,
		nextPinned,
		nextSynced,
	)
	if err != nil {
		return models.Conversation{}, err
	}

	return s.GetConversationByID(ctx, userID, conversationID)
}

func (s *Store) DeleteConversation(ctx context.Context, userID string, conversationID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`DELETE FROM conversations WHERE id = $1 AND user_id = $2`,
		conversationID,
		userID,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateConversationStats(ctx context.Context, userID string, conversationID string, preview string, messageCount int) error {
	if _, err := s.GetConversationByID(ctx, userID, conversationID); err != nil {
		return err
	}

	_, err := s.db.ExecContext(
		ctx,
		`UPDATE conversations
		 SET last_message_preview = $2,
		     message_count = $3,
		     is_cloud_synced = true,
		     updated_at = NOW()
		 WHERE id = $1 AND user_id = $4`,
		conversationID,
		buildPreview(preview),
		messageCount,
		userID,
	)
	return err
}

func (s *Store) ListLegacyConversationMessages(ctx context.Context, userID string, conversationID string) ([]models.ConversationMessage, error) {
	if _, err := s.GetConversationByID(ctx, userID, conversationID); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, conversation_id, role, content, created_at
		 FROM conversation_messages
		 WHERE conversation_id = $1
		 ORDER BY created_at ASC`,
		conversationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := make([]models.ConversationMessage, 0)
	for rows.Next() {
		var message models.ConversationMessage
		if err := rows.Scan(
			&message.ID,
			&message.ConversationID,
			&message.Role,
			&message.Content,
			&message.CreatedAt,
		); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func scanConversation(row rowScanner) (models.Conversation, error) {
	var conversation models.Conversation
	if err := row.Scan(
		&conversation.ID,
		&conversation.Title,
		&conversation.Pinned,
		&conversation.IsCloudSynced,
		&conversation.Channel,
		&conversation.ChannelThreadID,
		&conversation.ChannelDisplayName,
		&conversation.Provider,
		&conversation.Model,
		&conversation.LastMessagePreview,
		&conversation.MessageCount,
		&conversation.UpdatedAt,
	); err != nil {
		return models.Conversation{}, err
	}
	return conversation, nil
}

func fallbackTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return "New chat"
	}
	return title
}

func normalizeModelList(active string, models []string) []string {
	normalized := make([]string, 0, len(models)+1)
	seen := make(map[string]struct{}, len(models)+1)

	if trimmed := strings.TrimSpace(active); trimmed != "" {
		normalized = append(normalized, trimmed)
		seen[trimmed] = struct{}{}
	}

	for _, model := range models {
		trimmed := strings.TrimSpace(model)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		normalized = append(normalized, trimmed)
		seen[trimmed] = struct{}{}
	}

	if len(normalized) == 0 {
		normalized = append(normalized, "gpt-4o-mini")
	}

	return normalized
}

func buildPreview(content string) string {
	trimmed := strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if len(trimmed) <= 160 {
		return trimmed
	}
	return trimmed[:157] + "..."
}

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
