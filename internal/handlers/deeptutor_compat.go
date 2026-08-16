package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"be-miawai/internal/database"
	"be-miawai/internal/models"
)

type dtSessionPreferences struct {
	Capability       string          `json:"capability,omitempty"`
	Tools            []string        `json:"tools,omitempty"`
	KnowledgeBases   []string        `json:"knowledge_bases,omitempty"`
	Language         string          `json:"language,omitempty"`
	LLMSelection     *dtLLMSelection `json:"llm_selection,omitempty"`
	SelectedBranches map[string]int  `json:"selected_branches,omitempty"`
}

type dtSessionSummary struct {
	ID           string                `json:"id"`
	SessionID    string                `json:"session_id"`
	Title        string                `json:"title"`
	CreatedAt    int64                 `json:"created_at"`
	UpdatedAt    int64                 `json:"updated_at"`
	MessageCount int                   `json:"message_count"`
	LastMessage  string                `json:"last_message"`
	Status       string                `json:"status,omitempty"`
	ActiveTurnID string                `json:"active_turn_id,omitempty"`
	Preferences  *dtSessionPreferences `json:"preferences,omitempty"`
}

type dtSessionDetail struct {
	ID                string                `json:"id"`
	SessionID         string                `json:"session_id"`
	Title             string                `json:"title"`
	CreatedAt         int64                 `json:"created_at"`
	UpdatedAt         int64                 `json:"updated_at"`
	Status            string                `json:"status,omitempty"`
	ActiveTurnID      string                `json:"active_turn_id,omitempty"`
	CompressedSummary string                `json:"compressed_summary,omitempty"`
	SummaryUpToMsgID  int                   `json:"summary_up_to_msg_id,omitempty"`
	Preferences       *dtSessionPreferences `json:"preferences,omitempty"`
	Messages          []dtSessionMessage    `json:"messages"`
	ActiveTurns       []any                 `json:"active_turns,omitempty"`
}

type dtSessionMessage struct {
	ID              int             `json:"id"`
	SessionID       string          `json:"session_id"`
	Role            string          `json:"role"`
	Content         string          `json:"content"`
	Capability      string          `json:"capability,omitempty"`
	Events          []dtStreamEvent `json:"events"`
	Attachments     []dtAttachment  `json:"attachments"`
	Metadata        map[string]any  `json:"metadata,omitempty"`
	CreatedAt       int64           `json:"created_at"`
	ParentMessageID *int            `json:"parent_message_id,omitempty"`
}

type dtStreamEvent struct {
	Type      string         `json:"type"`
	Status    string         `json:"status,omitempty"`
	Title     string         `json:"title,omitempty"`
	Content   string         `json:"content,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt int64          `json:"created_at,omitempty"`
}

type dtAttachment struct {
	Type          string `json:"type"`
	Filename      string `json:"filename,omitempty"`
	Base64        string `json:"base64,omitempty"`
	URL           string `json:"url,omitempty"`
	MimeType      string `json:"mime_type,omitempty"`
	ID            string `json:"id,omitempty"`
	ExtractedText string `json:"extracted_text,omitempty"`
	Generated     bool   `json:"generated,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
}

type dtLLMSelection struct {
	ProfileID string `json:"profile_id"`
	ModelID   string `json:"model_id"`
}

type dtLLMOption struct {
	ProfileID       string `json:"profile_id"`
	ModelID         string `json:"model_id"`
	ProfileName     string `json:"profile_name"`
	ModelName       string `json:"model_name"`
	Model           string `json:"model"`
	Provider        string `json:"provider"`
	ProviderLabel   string `json:"provider_label"`
	ContextWindow   int    `json:"context_window"`
	IsActiveDefault bool   `json:"is_active_default"`
}

func (s *Server) dtListSessions(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessConversationsRead) {
		writeError(w, http.StatusForbidden, "session history is not allowed for this role")
		return
	}

	conversations, err := s.store.ListConversationsByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load sessions")
		return
	}

	limit := parseBoundedInt(r.URL.Query().Get("limit"), 50, 1, 200)
	offset := parseBoundedInt(r.URL.Query().Get("offset"), 0, 0, len(conversations))
	end := offset + limit
	if end > len(conversations) {
		end = len(conversations)
	}
	if offset > len(conversations) {
		offset = len(conversations)
	}

	sessions := make([]dtSessionSummary, 0, end-offset)
	for _, conversation := range conversations[offset:end] {
		sessions = append(sessions, s.dtSessionSummaryFromConversation(conversation))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": sessions,
		"limit":    limit,
		"offset":   offset,
		"total":    len(conversations),
	})
}

func (s *Server) dtGetSession(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessConversationsRead) || !hasAccess(user, accessChatRead) {
		writeError(w, http.StatusForbidden, "session history is not allowed for this role")
		return
	}

	session, err := s.dtLoadSessionDetail(r, user)
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) dtUpdateSession(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessConversationsWrite) {
		writeError(w, http.StatusForbidden, "session updates are not allowed for this role")
		return
	}

	var body struct {
		Title       *string               `json:"title"`
		Preferences *dtSessionPreferences `json:"preferences"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	conversationID := r.PathValue("id")
	if body.Title != nil {
		if _, err := s.store.UpdateConversationMeta(r.Context(), user.ID, conversationID, body.Title, nil, nil); err != nil {
			if database.IsNotFound(err) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to update session")
			return
		}
	}

	session, err := s.dtLoadSessionDetail(r, user)
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]dtSessionDetail{"session": session})
}

func (s *Server) dtDeleteSession(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessConversationsDelete) {
		writeError(w, http.StatusForbidden, "session deletion is not allowed for this role")
		return
	}

	conversationID := r.PathValue("id")
	if err := s.store.DeleteConversation(r.Context(), user.ID, conversationID); err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete session")
		return
	}
	_ = s.chatStorage.DeleteMessages(conversationID)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) dtRecordQuizResults(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessConversationsRead) {
		writeError(w, http.StatusForbidden, "session history is not allowed for this role")
		return
	}
	if _, err := s.store.GetConversationByID(r.Context(), user.ID, r.PathValue("id")); err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"recorded": true})
}

func (s *Server) dtDeleteMessage(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessConversationsWrite) {
		writeError(w, http.StatusForbidden, "message deletion is not allowed for this role")
		return
	}
	if _, err := s.store.GetConversationByID(r.Context(), user.ID, r.PathValue("id")); err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}
	if _, err := strconv.Atoi(strings.TrimSpace(r.PathValue("messageId"))); err != nil {
		writeError(w, http.StatusBadRequest, "message id must be numeric")
		return
	}
	messageID, _ := strconv.Atoi(strings.TrimSpace(r.PathValue("messageId")))
	conversation, err := s.store.GetConversationByID(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}
	messages, err := s.loadConversationMessages(r.Context(), user.ID, conversation)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load messages")
		return
	}
	idx := messageID - 1
	if idx < 0 || idx >= len(messages) {
		writeError(w, http.StatusNotFound, "message not found")
		return
	}
	messages = append(messages[:idx], messages[idx+1:]...)
	if err := s.chatStorage.SaveMessages(conversation.ID, messages); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete message")
		return
	}
	preview := ""
	if len(messages) > 0 {
		preview = messages[len(messages)-1].Content
	}
	_ = s.store.UpdateConversationStats(r.Context(), user.ID, conversation.ID, preview, len(messages))
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) dtUpdateBranchSelection(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessConversationsWrite) {
		writeError(w, http.StatusForbidden, "branch updates are not allowed for this role")
		return
	}
	if _, err := s.store.GetConversationByID(r.Context(), user.ID, r.PathValue("id")); err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}
	var body struct {
		SelectedBranches map[string]int `json:"selected_branches"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.SelectedBranches == nil {
		body.SelectedBranches = map[string]int{}
	}
	writeJSON(w, http.StatusOK, map[string]map[string]int{"selected_branches": body.SelectedBranches})
}

func (s *Server) dtLLMOptions(w http.ResponseWriter, r *http.Request, user models.User) {
	if !hasAccess(user, accessRuntimeRead) {
		writeError(w, http.StatusForbidden, "runtime access is not allowed for this role")
		return
	}
	settings, err := s.loadRuntimeSettings(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load llm options")
		return
	}
	settings = applyRuntimeDefaults(settings)

	activeModel := firstNonEmpty(strings.TrimSpace(settings.Models.Active), firstModel(settings.Models.All), "default")
	modelsList := settings.Models.All
	if len(modelsList) == 0 {
		modelsList = []string{activeModel}
	}
	provider := strings.TrimSpace(settings.Provider)
	providerLabel := providerDisplayName(provider)

	options := make([]dtLLMOption, 0, len(modelsList))
	for _, model := range modelsList {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		option := dtLLMOption{
			ProfileID:       "miaw-runtime",
			ModelID:         model,
			ProfileName:     "Miaw Runtime",
			ModelName:       model,
			Model:           model,
			Provider:        provider,
			ProviderLabel:   providerLabel,
			ContextWindow:   128000,
			IsActiveDefault: model == activeModel,
		}
		options = append(options, option)
	}
	if len(options) == 0 {
		options = append(options, dtLLMOption{
			ProfileID:       "miaw-runtime",
			ModelID:         activeModel,
			ProfileName:     "Miaw Runtime",
			ModelName:       activeModel,
			Model:           activeModel,
			Provider:        provider,
			ProviderLabel:   providerLabel,
			ContextWindow:   128000,
			IsActiveDefault: true,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"active":  dtLLMSelection{ProfileID: "miaw-runtime", ModelID: activeModel},
		"options": options,
	})
}

func (s *Server) dtCapabilities(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{
		"capabilities": []string{"llm"},
		"grants": map[string]bool{
			"llm": hasAccess(user, accessChatRead),
		},
	})
}

func (s *Server) dtLoadSessionDetail(r *http.Request, user models.User) (dtSessionDetail, error) {
	conversation, err := s.store.GetConversationByID(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		return dtSessionDetail{}, err
	}
	messages, err := s.loadConversationMessages(r.Context(), user.ID, conversation)
	if err != nil {
		return dtSessionDetail{}, err
	}
	return s.dtSessionDetailFromConversation(conversation, messages), nil
}

func (s *Server) dtSessionSummaryFromConversation(conversation models.Conversation) dtSessionSummary {
	updatedAt := unixMillis(conversation.UpdatedAt)
	return dtSessionSummary{
		ID:           conversation.ID,
		SessionID:    conversation.ID,
		Title:        conversation.Title,
		CreatedAt:    updatedAt,
		UpdatedAt:    updatedAt,
		MessageCount: conversation.MessageCount,
		LastMessage:  conversation.LastMessagePreview,
		Status:       "idle",
		Preferences:  s.dtPreferencesFromConversation(conversation),
	}
}

func (s *Server) dtSessionDetailFromConversation(conversation models.Conversation, messages []models.ConversationMessage) dtSessionDetail {
	summary := s.dtSessionSummaryFromConversation(conversation)
	dtMessages := make([]dtSessionMessage, 0, len(messages))
	for idx, message := range messages {
		dtMessages = append(dtMessages, dtMessageFromConversationMessage(conversation.ID, idx+1, message))
	}
	return dtSessionDetail{
		ID:          summary.ID,
		SessionID:   summary.SessionID,
		Title:       summary.Title,
		CreatedAt:   summary.CreatedAt,
		UpdatedAt:   summary.UpdatedAt,
		Status:      summary.Status,
		Preferences: summary.Preferences,
		Messages:    dtMessages,
		ActiveTurns: []any{},
	}
}

func (s *Server) dtPreferencesFromConversation(conversation models.Conversation) *dtSessionPreferences {
	capability := "chat"
	tools := []string{}
	if strings.Contains(strings.ToLower(conversation.LastMessagePreview), "research") {
		capability = "deep_research"
		tools = append(tools, "web_search")
	}
	model := firstNonEmpty(strings.TrimSpace(conversation.Model), firstModel(s.cfg.DefaultProviderModels), "default")
	return &dtSessionPreferences{
		Capability:   capability,
		Tools:        tools,
		Language:     "id",
		LLMSelection: &dtLLMSelection{ProfileID: "miaw-runtime", ModelID: model},
	}
}

func dtMessageFromConversationMessage(sessionID string, id int, message models.ConversationMessage) dtSessionMessage {
	capability := "chat"
	events := []dtStreamEvent{}
	metadata := map[string]any{}
	if message.ResearchReport != nil {
		capability = "deep_research"
		metadata["research_report"] = message.ResearchReport
		events = append(events, dtStreamEvent{
			Type:      "result",
			Status:    "completed",
			Title:     "Research complete",
			Metadata:  map[string]any{"report": message.ResearchReport},
			CreatedAt: unixMillis(message.CreatedAt),
		})
	}
	if len(metadata) == 0 {
		metadata = nil
	}

	attachments := make([]dtAttachment, 0, len(message.ImageURLs))
	for idx, imageURL := range message.ImageURLs {
		attachments = append(attachments, dtAttachment{
			Type: "image",
			URL:  imageURL,
			ID:   "att-" + strconv.Itoa(id) + "-" + strconv.Itoa(idx+1),
		})
	}

	return dtSessionMessage{
		ID:          id,
		SessionID:   sessionID,
		Role:        normalizeDTRole(message.Role),
		Content:     message.Content,
		Capability:  capability,
		Events:      events,
		Attachments: attachments,
		Metadata:    metadata,
		CreatedAt:   unixMillis(message.CreatedAt),
	}
}

func normalizeDTRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user", "assistant", "system":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return "assistant"
	}
}

func parseBoundedInt(raw string, fallback int, min int, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		value = fallback
	}
	if value < min {
		return min
	}
	if max >= min && value > max {
		return max
	}
	return value
}

func providerDisplayName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		return "OpenAI"
	case "anthropic":
		return "Anthropic"
	case "google", "gemini":
		return "Google"
	case "openai-compatible", "compatible":
		return "OpenAI Compatible"
	case "":
		return "Miaw"
	default:
		return strings.Title(strings.ReplaceAll(provider, "-", " "))
	}
}

func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return time.Now().UTC().UnixMilli()
	}
	return t.UTC().UnixMilli()
}
