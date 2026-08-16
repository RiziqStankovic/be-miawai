package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"be-miawai/internal/models"
)

var dtMemorySurfaces = map[string]string{
	"chat":     "Chat",
	"notebook": "Notebook",
	"quiz":     "Quiz",
	"kb":       "Knowledge",
	"book":     "Book",
	"partner":  "Partner",
	"cowriter": "Co-Writer",
}

var dtMemorySlots = map[string]string{
	"profile": "Profile",
	"recent":  "Recent",
	"scope":   "Scope",
}

func (s *Server) dtMemorySnapshot(w http.ResponseWriter, r *http.Request, user models.User) {
	surface := strings.ToLower(strings.TrimSpace(r.PathValue("slot")))
	if surface == "" {
		surface = strings.ToLower(strings.TrimSpace(r.PathValue("surface")))
	}
	if _, ok := dtMemorySurfaces[surface]; !ok {
		writeJSON(w, http.StatusOK, map[string][]any{"entities": []any{}})
		return
	}

	memories, err := s.store.ListMemoriesByUser(r.Context(), user.ID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load memory snapshot")
		return
	}

	entities := make([]map[string]any, 0, len(memories))
	for _, memory := range memories {
		if !memoryMatchesSurface(memory, surface) {
			continue
		}
		entities = append(entities, map[string]any{
			"id":      firstNonEmpty(memory.ID, fmt.Sprintf("memory_%d", len(entities)+1)),
			"label":   firstNonEmpty(memory.Title, memory.Kind, dtMemorySurfaces[surface]),
			"ts":      memory.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			"content": memory.Content,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entities": entities})
}

func (s *Server) dtMemoryDoc(w http.ResponseWriter, r *http.Request, user models.User) {
	layer := strings.ToUpper(strings.TrimSpace(r.PathValue("layer")))
	slot := strings.ToLower(strings.TrimSpace(r.PathValue("slot")))
	if layer != "L2" && layer != "L3" {
		writeJSON(w, http.StatusOK, map[string]string{"content": ""})
		return
	}

	memories, err := s.store.ListMemoriesByUser(r.Context(), user.ID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load memory document")
		return
	}

	if layer == "L2" {
		if _, ok := dtMemorySurfaces[slot]; !ok {
			writeJSON(w, http.StatusOK, map[string]string{"content": ""})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"content": buildMemoryLayerDoc(layer, slot, dtMemorySurfaces[slot], memories)})
		return
	}

	if _, ok := dtMemorySlots[slot]; !ok {
		writeJSON(w, http.StatusOK, map[string]string{"content": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": buildMemoryLayerDoc(layer, slot, dtMemorySlots[slot], memories)})
}

func buildMemoryLayerDoc(layer, key, label string, memories []models.UserMemory) string {
	var builder strings.Builder
	builder.WriteString("# ")
	builder.WriteString(layer)
	builder.WriteString(" ")
	builder.WriteString(label)
	builder.WriteString(" Memory\n\n")
	builder.WriteString("## ")
	builder.WriteString(label)
	builder.WriteString("\n")

	count := 0
	for _, memory := range memories {
		if layer == "L2" && !memoryMatchesSurface(memory, key) {
			continue
		}
		if layer == "L3" && !memoryMatchesSlot(memory, key) {
			continue
		}
		count++
		id := strings.TrimSpace(memory.ID)
		if id == "" {
			id = fmt.Sprintf("memory_%d", count)
		}
		text := strings.Join(strings.Fields(firstNonEmpty(memory.Title, memory.Content)), " ")
		if text == "" {
			continue
		}
		builder.WriteString("- ")
		builder.WriteString(text)
		builder.WriteString(" [^1] <!--")
		builder.WriteString(id)
		builder.WriteString("-->\n")
	}
	if count == 0 {
		builder.WriteString("- No saved Miaw memory yet. [^1] <!--memory_empty_compat-->\n")
	}
	builder.WriteString("\n[^1]: ")
	if layer == "L2" {
		builder.WriteString(key)
		builder.WriteString(":memory")
	} else {
		builder.WriteString("chat:memory")
	}
	builder.WriteString("\n")
	return builder.String()
}

func memoryMatchesSurface(memory models.UserMemory, surface string) bool {
	text := strings.ToLower(strings.TrimSpace(memory.Domain + " " + memory.Kind + " " + memory.Title + " " + memory.SourceConversationID))
	switch surface {
	case "chat":
		return text == "" || strings.Contains(text, "chat") || memory.SourceConversationID != ""
	case "notebook":
		return strings.Contains(text, "notebook")
	case "quiz":
		return strings.Contains(text, "quiz") || strings.Contains(text, "question")
	case "kb":
		return strings.Contains(text, "knowledge") || strings.Contains(text, "kb")
	case "book":
		return strings.Contains(text, "book") || strings.Contains(text, "learning")
	case "partner":
		return strings.Contains(text, "partner") || strings.Contains(text, "agent")
	case "cowriter":
		return strings.Contains(text, "cowriter") || strings.Contains(text, "co-writer") || strings.Contains(text, "writer")
	default:
		return false
	}
}

func memoryMatchesSlot(memory models.UserMemory, slot string) bool {
	text := strings.ToLower(strings.TrimSpace(memory.Domain + " " + memory.Kind + " " + memory.Title))
	switch slot {
	case "profile":
		return strings.Contains(text, "profile") || strings.Contains(text, "preference") || strings.Contains(text, "identity") || strings.Contains(text, "persona")
	case "recent":
		return true
	case "scope":
		return strings.Contains(text, "scope") || strings.Contains(text, "project") || strings.Contains(text, "goal")
	default:
		return false
	}
}

func (s *Server) dtCodexCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html><html><head><title>DeepTutor Codex</title></head><body><script>window.close();</script><p>OpenAI Codex compatibility callback received. You can close this window.</p></body></html>`))
}

func (s *Server) dtCodexStatus(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, dtCodexStatusPayload("disconnected", nil))
}

func (s *Server) dtCodexStart(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{
		"operation_id":          "compat-codex-oauth",
		"authorize_url":         "",
		"expires_in":            0,
		"callback_port":         0,
		"callback_forward_port": 0,
		"redirect_uri":          "",
		"ssh_forward_command":   "",
	})
}

func (s *Server) dtCodexNoop(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, dtCodexStatusPayload("disconnected", nil))
}

func dtCodexStatusPayload(connection string, errCode any) map[string]any {
	return map[string]any{
		"connection":            connection,
		"operation_id":          nil,
		"operation_state":       nil,
		"authorize_url":         nil,
		"expires_in":            nil,
		"callback_port":         nil,
		"callback_forward_port": nil,
		"redirect_uri":          nil,
		"model_count":           0,
		"catalog_source":        nil,
		"catalog_fetched_at":    nil,
		"active_model":          nil,
		"models":                []any{},
		"activated":             false,
		"error_code":            errCode,
	}
}

func (s *Server) dtCodeBuddyStatus(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, dtCodeBuddyStatusPayload("disconnected", nil))
}

func (s *Server) dtCodeBuddyStart(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, dtCodeBuddyStatusPayload("authorizing", nil))
}

func (s *Server) dtCodeBuddyNoop(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, dtCodeBuddyStatusPayload("disconnected", nil))
}

func dtCodeBuddyStatusPayload(connection string, errCode any) map[string]any {
	operationState := any(nil)
	if connection == "authorizing" {
		operationState = "waiting"
	}
	return map[string]any{
		"connection":      connection,
		"operation_state": operationState,
		"authorize_url":   nil,
		"user_label":      nil,
		"error_code":      errCode,
	}
}
