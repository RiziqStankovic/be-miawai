package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"be-miawai/internal/database"
	"be-miawai/internal/models"
)

func (s *Server) dtAuthStatus(w http.ResponseWriter, r *http.Request, user models.User) {
	isAdmin := strings.EqualFold(user.Role, "admin")
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":       true,
		"authenticated": true,
		"user_id":       user.ID,
		"username":      firstNonEmpty(user.Email, user.Name),
		"role":          firstNonEmpty(user.Role, "user"),
		"is_admin":      isAdmin,
		"avatar":        "",
	})
}

func (s *Server) dtRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	username := strings.TrimSpace(body.Username)
	if username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if !strings.Contains(username, "@") {
		username += "@deeptutor.local"
	}
	r.Body = newJSONBody(map[string]string{
		"email":    username,
		"name":     strings.Split(username, "@")[0],
		"password": body.Password,
	})
	s.register(w, r)
}

func (s *Server) dtLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	login := firstNonEmpty(strings.TrimSpace(body.Email), strings.TrimSpace(body.Username))
	if login != "" && !strings.Contains(login, "@") {
		login += "@deeptutor.local"
	}
	r.Body = newJSONBody(map[string]string{"email": login, "password": body.Password})
	s.passwordLogin(w, r)
}

func (s *Server) dtIsFirstUser(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"is_first_user": false})
}

func (s *Server) dtAttachmentLimits(w http.ResponseWriter, r *http.Request, user models.User) {
	const maxFileBytes = 25 * 1024 * 1024
	const maxTotalBytes = 50 * 1024 * 1024
	writeJSON(w, http.StatusOK, map[string]any{
		"effective": map[string]int{
			"max_file_bytes":  maxFileBytes,
			"max_total_bytes": maxTotalBytes,
		},
	})
}

type dtCoWriterDocument struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	Preview   string `json:"preview,omitempty"`
}

func (s *Server) dtListCoWriterDocuments(w http.ResponseWriter, r *http.Request, user models.User) {
	conversations, err := s.store.ListConversationsByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load documents")
		return
	}
	docs := make([]dtCoWriterDocument, 0, len(conversations))
	for _, conversation := range conversations {
		if strings.HasPrefix(conversation.Title, "Co-Writer:") {
			docs = append(docs, dtCoWriterDocumentFromConversation(conversation, ""))
		}
	}
	writeJSON(w, http.StatusOK, map[string][]dtCoWriterDocument{"documents": docs})
}

func (s *Server) dtCreateCoWriterDocument(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	title := firstNonEmpty(strings.TrimSpace(body.Title), "Untitled document")
	settings, _ := s.loadRuntimeSettings(r.Context(), user.ID)
	conversation, err := s.store.CreateConversation(r.Context(), user.ID, "Co-Writer: "+title, settings.Provider, settings.Models.Active)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create document")
		return
	}
	if strings.TrimSpace(body.Content) != "" {
		messages := []models.ConversationMessage{newConversationMessage(conversation.ID, "assistant", body.Content)}
		_ = s.chatStorage.SaveMessages(conversation.ID, messages)
		_ = s.store.UpdateConversationStats(r.Context(), user.ID, conversation.ID, body.Content, len(messages))
	}
	writeJSON(w, http.StatusOK, dtCoWriterDocumentFromConversation(conversation, body.Content))
}

func (s *Server) dtGetCoWriterDocument(w http.ResponseWriter, r *http.Request, user models.User) {
	conversation, content, ok := s.loadCoWriterDocument(w, r, user)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, dtCoWriterDocumentFromConversation(conversation, content))
}

func (s *Server) dtUpdateCoWriterDocument(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Title   *string `json:"title"`
		Content *string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	conversationID := r.PathValue("id")
	if body.Title != nil {
		title := "Co-Writer: " + strings.TrimPrefix(strings.TrimSpace(*body.Title), "Co-Writer: ")
		if _, err := s.store.UpdateConversationMeta(r.Context(), user.ID, conversationID, &title, nil, nil); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update document")
			return
		}
	}
	conversation, err := s.store.GetConversationByID(r.Context(), user.ID, conversationID)
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "document not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load document")
		return
	}
	content := ""
	if body.Content != nil {
		content = *body.Content
		messages := []models.ConversationMessage{newConversationMessage(conversation.ID, "assistant", content)}
		if err := s.chatStorage.SaveMessages(conversation.ID, messages); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update document")
			return
		}
		_ = s.store.UpdateConversationStats(r.Context(), user.ID, conversation.ID, content, len(messages))
	} else {
		messages, _ := s.loadConversationMessages(r.Context(), user.ID, conversation)
		if len(messages) > 0 {
			content = messages[len(messages)-1].Content
		}
	}
	conversation, _ = s.store.GetConversationByID(r.Context(), user.ID, conversationID)
	writeJSON(w, http.StatusOK, dtCoWriterDocumentFromConversation(conversation, content))
}

func (s *Server) dtDeleteCoWriterDocument(w http.ResponseWriter, r *http.Request, user models.User) {
	if err := s.store.DeleteConversation(r.Context(), user.ID, r.PathValue("id")); err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "document not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete document")
		return
	}
	_ = s.chatStorage.DeleteMessages(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) loadCoWriterDocument(w http.ResponseWriter, r *http.Request, user models.User) (models.Conversation, string, bool) {
	conversation, err := s.store.GetConversationByID(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		if database.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "document not found")
			return models.Conversation{}, "", false
		}
		writeError(w, http.StatusInternalServerError, "failed to load document")
		return models.Conversation{}, "", false
	}
	messages, err := s.loadConversationMessages(r.Context(), user.ID, conversation)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load document")
		return models.Conversation{}, "", false
	}
	content := ""
	if len(messages) > 0 {
		content = messages[len(messages)-1].Content
	}
	return conversation, content, true
}

func dtCoWriterDocumentFromConversation(conversation models.Conversation, content string) dtCoWriterDocument {
	title := strings.TrimSpace(strings.TrimPrefix(conversation.Title, "Co-Writer:"))
	if title == "" {
		title = conversation.Title
	}
	return dtCoWriterDocument{
		ID:        conversation.ID,
		Title:     title,
		Content:   content,
		CreatedAt: unixMillis(conversation.UpdatedAt),
		UpdatedAt: unixMillis(conversation.UpdatedAt),
		Preview:   dtPreview(content),
	}
}

type dtImportMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	CreatedAt int64          `json:"created_at"`
	Metadata  map[string]any `json:"metadata"`
}

type dtImportSession struct {
	ExternalID string            `json:"external_id"`
	Title      string            `json:"title"`
	SourceCWD  string            `json:"source_cwd"`
	CreatedAt  int64             `json:"created_at"`
	UpdatedAt  int64             `json:"updated_at"`
	Messages   []dtImportMessage `json:"messages"`
}

func (s *Server) dtImportChatHistory(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Source    string            `json:"source"`
		Sessions  []dtImportSession `json:"sessions"`
		AgentID   string            `json:"agent_id"`
		AgentName string            `json:"agent_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	settings, _ := s.loadRuntimeSettings(r.Context(), user.ID)
	outcomes := make([]map[string]any, 0, len(body.Sessions))
	imported := 0
	for _, session := range body.Sessions {
		if strings.TrimSpace(session.ExternalID) == "" || len(session.Messages) == 0 {
			outcomes = append(outcomes, map[string]any{"external_id": session.ExternalID, "imported": false, "reason": "empty session"})
			continue
		}
		conversation, err := s.store.CreateConversation(r.Context(), user.ID, firstNonEmpty(session.Title, "Imported chat"), settings.Provider, settings.Models.Active)
		if err != nil {
			outcomes = append(outcomes, map[string]any{"external_id": session.ExternalID, "imported": false, "reason": "create failed"})
			continue
		}
		messages := make([]models.ConversationMessage, 0, len(session.Messages))
		for _, msg := range session.Messages {
			createdAt := time.Now().UTC()
			if msg.CreatedAt > 0 {
				createdAt = time.Unix(msg.CreatedAt, 0).UTC()
			}
			messages = append(messages, models.ConversationMessage{ID: database.NewID("cmsg"), ConversationID: conversation.ID, Role: normalizeDTRole(msg.Role), Content: msg.Content, CreatedAt: createdAt})
		}
		if err := s.chatStorage.SaveMessages(conversation.ID, messages); err != nil {
			outcomes = append(outcomes, map[string]any{"external_id": session.ExternalID, "imported": false, "reason": "save failed"})
			continue
		}
		preview := ""
		if len(messages) > 0 {
			preview = messages[len(messages)-1].Content
		}
		_ = s.store.UpdateConversationStats(r.Context(), user.ID, conversation.ID, preview, len(messages))
		outcomes = append(outcomes, map[string]any{"external_id": session.ExternalID, "session_id": conversation.ID, "imported": true})
		imported++
	}
	writeJSON(w, http.StatusOK, map[string]any{"imported": imported, "skipped": len(body.Sessions) - imported, "sessions": outcomes})
}

func (s *Server) dtListImportedSessions(w http.ResponseWriter, r *http.Request, user models.User) {
	s.dtListSessions(w, r, user)
}

func dtPreview(content string) string {
	content = strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if len(content) <= 160 {
		return content
	}
	return content[:157] + "..."
}

func newJSONBody(value any) io.ReadCloser {
	data, _ := json.Marshal(value)
	return io.NopCloser(bytes.NewReader(data))
}
