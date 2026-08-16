package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"be-miawai/internal/database"
	"be-miawai/internal/models"
)

var dtState = struct {
	sync.Mutex
	notebooks          map[string]map[string]*dtNotebook
	questionEntries    map[string]map[int]*dtQuestionEntry
	questionCategories map[string]map[int]*dtQuestionCategory
	learning           map[string]map[string]*dtLearningProgress
	books              map[string]map[string]*dtBookBundle
	nextQuestionID     int
	nextCategoryID     int
}{
	notebooks:          map[string]map[string]*dtNotebook{},
	questionEntries:    map[string]map[int]*dtQuestionEntry{},
	questionCategories: map[string]map[int]*dtQuestionCategory{},
	learning:           map[string]map[string]*dtLearningProgress{},
	books:              map[string]map[string]*dtBookBundle{},
	nextQuestionID:     1,
	nextCategoryID:     1,
}

type dtNotebook struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Color       string             `json:"color,omitempty"`
	Icon        string             `json:"icon,omitempty"`
	RecordCount int                `json:"record_count,omitempty"`
	CreatedAt   int64              `json:"created_at,omitempty"`
	UpdatedAt   int64              `json:"updated_at,omitempty"`
	Records     []dtNotebookRecord `json:"records,omitempty"`
}

type dtNotebookRecord struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Summary   string         `json:"summary,omitempty"`
	UserQuery string         `json:"user_query"`
	Output    string         `json:"output"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt int64          `json:"created_at,omitempty"`
	KBName    *string        `json:"kb_name,omitempty"`
}

type dtQuestionEntry struct {
	ID                int                  `json:"id"`
	SessionID         string               `json:"session_id"`
	SessionTitle      string               `json:"session_title"`
	TurnID            string               `json:"turn_id"`
	QuestionID        string               `json:"question_id"`
	Question          string               `json:"question"`
	QuestionType      string               `json:"question_type"`
	Options           map[string]string    `json:"options"`
	CorrectAnswer     string               `json:"correct_answer"`
	Explanation       string               `json:"explanation"`
	Difficulty        string               `json:"difficulty"`
	UserAnswer        string               `json:"user_answer"`
	IsCorrect         bool                 `json:"is_correct"`
	Bookmarked        bool                 `json:"bookmarked"`
	FollowupSessionID string               `json:"followup_session_id"`
	AIJudgment        string               `json:"ai_judgment,omitempty"`
	CreatedAt         int64                `json:"created_at"`
	UpdatedAt         int64                `json:"updated_at"`
	Categories        []dtQuestionCategory `json:"categories,omitempty"`
}

type dtQuestionCategory struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"created_at"`
	EntryCount int    `json:"entry_count"`
}

type dtLearningProgress struct {
	BookID          string             `json:"book_id"`
	Modules         []dtLearningModule `json:"modules"`
	MasteryLevels   map[string]int     `json:"mastery_levels"`
	CurrentModuleID string             `json:"current_module_id,omitempty"`
	CurrentStage    string             `json:"current_stage,omitempty"`
	Diagnostic      any                `json:"diagnostic,omitempty"`
	UpdatedAt       int64              `json:"-"`
}

type dtLearningModule struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	Order           int                `json:"order"`
	PassThreshold   int                `json:"pass_threshold"`
	KnowledgePoints []dtKnowledgePoint `json:"knowledge_points"`
}

type dtKnowledgePoint struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	ModuleID string `json:"module_id,omitempty"`
}

type dtBook struct {
	ID             string         `json:"id"`
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	Status         string         `json:"status"`
	Proposal       map[string]any `json:"proposal"`
	KnowledgeBases []string       `json:"knowledge_bases"`
	Language       string         `json:"language"`
	PageCount      int            `json:"page_count"`
	ChapterCount   int            `json:"chapter_count"`
	CreatedAt      int64          `json:"created_at"`
	UpdatedAt      int64          `json:"updated_at"`
	Metadata       map[string]any `json:"metadata"`
}

type dtBookBundle struct {
	Book     dtBook           `json:"book"`
	Spine    map[string]any   `json:"spine"`
	Pages    []map[string]any `json:"pages"`
	Progress map[string]any   `json:"progress"`
}

func (s *Server) dtListNotebooks(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	defer dtState.Unlock()
	items := make([]dtNotebook, 0, len(userNotebooks(user.ID)))
	for _, nb := range userNotebooks(user.ID) {
		copy := *nb
		copy.RecordCount = len(copy.Records)
		copy.Records = nil
		items = append(items, copy)
	}
	writeJSON(w, http.StatusOK, map[string][]dtNotebook{"notebooks": items})
}

func (s *Server) dtCreateNotebook(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct{ Name, Description, Color, Icon string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	now := time.Now().UnixMilli()
	nb := &dtNotebook{ID: database.NewID("nb"), Name: firstNonEmpty(strings.TrimSpace(body.Name), "Notebook"), Description: body.Description, Color: firstNonEmpty(body.Color, "#6366F1"), Icon: firstNonEmpty(body.Icon, "book"), CreatedAt: now, UpdatedAt: now, Records: []dtNotebookRecord{}}
	dtState.Lock()
	userNotebooks(user.ID)[nb.ID] = nb
	dtState.Unlock()
	writeJSON(w, http.StatusOK, map[string]dtNotebook{"notebook": *nb})
}

func (s *Server) dtGetNotebook(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	nb := userNotebooks(user.ID)[r.PathValue("id")]
	dtState.Unlock()
	if nb == nil {
		writeError(w, http.StatusNotFound, "notebook not found")
		return
	}
	copy := *nb
	copy.RecordCount = len(copy.Records)
	writeJSON(w, http.StatusOK, copy)
}

func (s *Server) dtUpdateNotebook(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct{ Name, Description, Color, Icon string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	dtState.Lock()
	nb := userNotebooks(user.ID)[r.PathValue("id")]
	if nb != nil {
		if strings.TrimSpace(body.Name) != "" {
			nb.Name = strings.TrimSpace(body.Name)
		}
		if body.Description != "" {
			nb.Description = body.Description
		}
		if body.Color != "" {
			nb.Color = body.Color
		}
		if body.Icon != "" {
			nb.Icon = body.Icon
		}
		nb.UpdatedAt = time.Now().UnixMilli()
	}
	dtState.Unlock()
	if nb == nil {
		writeError(w, http.StatusNotFound, "notebook not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]dtNotebook{"notebook": *nb})
}

func (s *Server) dtDeleteNotebook(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	delete(userNotebooks(user.ID), r.PathValue("id"))
	dtState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) dtDeleteNotebookRecord(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	nb := userNotebooks(user.ID)[r.PathValue("id")]
	if nb != nil {
		out := nb.Records[:0]
		for _, rec := range nb.Records {
			if rec.ID != r.PathValue("recordId") {
				out = append(out, rec)
			}
		}
		nb.Records = out
		nb.UpdatedAt = time.Now().UnixMilli()
	}
	dtState.Unlock()
	if nb == nil {
		writeError(w, http.StatusNotFound, "notebook not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) dtListQuestionEntries(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	defer dtState.Unlock()
	items := make([]dtQuestionEntry, 0, len(userQuestionEntries(user.ID)))
	for _, entry := range userQuestionEntries(user.ID) {
		items = append(items, *entry)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt > items[j].UpdatedAt })
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) dtGetQuestionEntry(w http.ResponseWriter, r *http.Request, user models.User) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	dtState.Lock()
	entry := userQuestionEntries(user.ID)[id]
	dtState.Unlock()
	if entry == nil {
		writeError(w, http.StatusNotFound, "entry not found")
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) dtLookupQuestionEntry(w http.ResponseWriter, r *http.Request, user models.User) {
	sessionID, questionID := r.URL.Query().Get("session_id"), r.URL.Query().Get("question_id")
	dtState.Lock()
	defer dtState.Unlock()
	for _, entry := range userQuestionEntries(user.ID) {
		if entry.SessionID == sessionID && entry.QuestionID == questionID {
			writeJSON(w, http.StatusOK, entry)
			return
		}
	}
	if r.URL.Query().Get("missing_ok") == "true" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeError(w, http.StatusNotFound, "entry not found")
}

func (s *Server) dtUpsertQuestionEntry(w http.ResponseWriter, r *http.Request, user models.User) {
	var body dtQuestionEntry
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	now := time.Now().UnixMilli()
	dtState.Lock()
	defer dtState.Unlock()
	entries := userQuestionEntries(user.ID)
	for _, entry := range entries {
		if entry.SessionID == body.SessionID && entry.QuestionID == body.QuestionID {
			body.ID = entry.ID
			body.CreatedAt = entry.CreatedAt
			body.UpdatedAt = now
			*entry = body
			writeJSON(w, http.StatusOK, entry)
			return
		}
	}
	body.ID = dtState.nextQuestionID
	dtState.nextQuestionID++
	body.CreatedAt = now
	body.UpdatedAt = now
	if body.Options == nil {
		body.Options = map[string]string{}
	}
	entries[body.ID] = &body
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) dtUpdateQuestionEntry(w http.ResponseWriter, r *http.Request, user models.User) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	dtState.Lock()
	entry := userQuestionEntries(user.ID)[id]
	if entry != nil {
		if v, ok := body["bookmarked"].(bool); ok {
			entry.Bookmarked = v
		}
		if v, ok := body["followup_session_id"].(string); ok {
			entry.FollowupSessionID = v
		}
		if v, ok := body["ai_judgment"].(string); ok {
			entry.AIJudgment = v
		}
		entry.UpdatedAt = time.Now().UnixMilli()
	}
	dtState.Unlock()
	if entry == nil {
		writeError(w, http.StatusNotFound, "entry not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
}

func (s *Server) dtDeleteQuestionEntry(w http.ResponseWriter, r *http.Request, user models.User) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	dtState.Lock()
	delete(userQuestionEntries(user.ID), id)
	dtState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
func (s *Server) dtAddEntryCategory(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]bool{"added": true})
}
func (s *Server) dtRemoveEntryCategory(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
}

func (s *Server) dtListQuestionCategories(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	defer dtState.Unlock()
	cats := []dtQuestionCategory{}
	for _, c := range userQuestionCategories(user.ID) {
		cats = append(cats, *c)
	}
	writeJSON(w, http.StatusOK, cats)
}
func (s *Server) dtCreateQuestionCategory(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dtState.Lock()
	id := dtState.nextCategoryID
	dtState.nextCategoryID++
	cat := &dtQuestionCategory{ID: id, Name: firstNonEmpty(body.Name, "Category"), CreatedAt: time.Now().UnixMilli()}
	userQuestionCategories(user.ID)[id] = cat
	dtState.Unlock()
	writeJSON(w, http.StatusOK, cat)
}
func (s *Server) dtRenameQuestionCategory(w http.ResponseWriter, r *http.Request, user models.User) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dtState.Lock()
	if cat := userQuestionCategories(user.ID)[id]; cat != nil && strings.TrimSpace(body.Name) != "" {
		cat.Name = body.Name
	}
	dtState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
}
func (s *Server) dtDeleteQuestionCategory(w http.ResponseWriter, r *http.Request, user models.User) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	dtState.Lock()
	delete(userQuestionCategories(user.ID), id)
	dtState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) dtLearningList(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	defer dtState.Unlock()
	summaries := []map[string]any{}
	for _, p := range userLearning(user.ID) {
		kp := 0
		for _, m := range p.Modules {
			kp += len(m.KnowledgePoints)
		}
		summaries = append(summaries, map[string]any{"book_id": p.BookID, "name": p.BookID, "modules_count": len(p.Modules), "kp_count": kp, "current_stage": firstNonEmpty(p.CurrentStage, "new"), "avg_mastery_pct": 0, "updated_at": p.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"summaries": summaries, "errors": []any{}})
}
func (s *Server) dtLearningGet(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	p := ensureLearning(user.ID, r.PathValue("bookId"))
	dtState.Unlock()
	writeJSON(w, http.StatusOK, p)
}
func (s *Server) dtLearningInit(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Modules []dtLearningModule `json:"modules"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dtState.Lock()
	p := ensureLearning(user.ID, r.PathValue("bookId"))
	p.Modules = body.Modules
	p.MasteryLevels = map[string]int{}
	for _, m := range body.Modules {
		for _, kp := range m.KnowledgePoints {
			p.MasteryLevels[kp.ID] = 0
		}
	}
	if len(body.Modules) > 0 {
		p.CurrentModuleID = body.Modules[0].ID
	}
	p.CurrentStage = "learning"
	p.UpdatedAt = time.Now().UnixMilli()
	dtState.Unlock()
	writeJSON(w, http.StatusOK, p)
}
func (s *Server) dtLearningMap(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	p := ensureLearning(user.ID, r.PathValue("bookId"))
	dtState.Unlock()
	modules := []map[string]any{}
	total := 0
	for _, m := range p.Modules {
		kps := []map[string]any{}
		for _, kp := range m.KnowledgePoints {
			mastery := p.MasteryLevels[kp.ID]
			status := "new"
			if mastery >= 80 {
				status = "mastered"
			} else if mastery > 0 {
				status = "learning"
			}
			kps = append(kps, map[string]any{"id": kp.ID, "name": kp.Name, "type": kp.Type, "status": status, "mastery": mastery})
			total++
		}
		modules = append(modules, map[string]any{"id": m.ID, "name": m.Name, "order": m.Order, "mastered": 0, "total": len(kps), "knowledge_points": kps})
	}
	writeJSON(w, http.StatusOK, map[string]any{"book_id": p.BookID, "next": map[string]any{"action": "learn", "knowledge_point_name": "Start", "knowledge_point_type": "concept", "status": "new", "mastery": 0, "threshold": 80, "reason": "Continue learning"}, "map": map[string]any{"counts": map[string]int{"mastered": 0, "learning": 0, "new": total, "total": total}, "due_reviews": 0, "complete": total == 0, "modules": modules}})
}
func (s *Server) dtLearningDelete(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	delete(userLearning(user.ID), r.PathValue("bookId"))
	dtState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
func (s *Server) dtLearningRedo(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	p := ensureLearning(user.ID, r.PathValue("bookId"))
	for k := range p.MasteryLevels {
		p.MasteryLevels[k] = 0
	}
	p.CurrentStage = "learning"
	dtState.Unlock()
	writeJSON(w, http.StatusOK, p)
}
func (s *Server) dtLearningImportFromBook(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]bool{"imported": true})
}
func (s *Server) dtLearningGenerateFromNotebook(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string][]dtLearningModule{"modules": {}})
}

func (s *Server) dtBookList(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	defer dtState.Unlock()
	books := []dtBook{}
	for _, b := range userBooks(user.ID) {
		books = append(books, b.Book)
	}
	writeJSON(w, http.StatusOK, map[string][]dtBook{"books": books})
}
func (s *Server) dtBookGet(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	b := userBooks(user.ID)[r.PathValue("bookId")]
	dtState.Unlock()
	if b == nil {
		writeError(w, http.StatusNotFound, "book not found")
		return
	}
	writeJSON(w, http.StatusOK, b)
}
func (s *Server) dtBookDelete(w http.ResponseWriter, r *http.Request, user models.User) {
	id := r.PathValue("bookId")
	dtState.Lock()
	delete(userBooks(user.ID), id)
	dtState.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "book_id": id})
}
func (s *Server) dtBookSpine(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	b := userBooks(user.ID)[r.PathValue("bookId")]
	dtState.Unlock()
	if b == nil {
		writeError(w, http.StatusNotFound, "book not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"spine": b.Spine})
}
func (s *Server) dtBookPage(w http.ResponseWriter, r *http.Request, user models.User) {
	dtState.Lock()
	b := userBooks(user.ID)[r.PathValue("bookId")]
	dtState.Unlock()
	if b == nil {
		writeError(w, http.StatusNotFound, "book not found")
		return
	}
	for _, p := range b.Pages {
		if p["id"] == r.PathValue("pageId") {
			writeJSON(w, http.StatusOK, map[string]any{"page": p})
			return
		}
	}
	writeError(w, http.StatusNotFound, "page not found")
}
func (s *Server) dtBookConfirmSpine(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string][]any{"pages": []any{}})
}
func (s *Server) dtBookOK(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (s *Server) dtBookHealth(w http.ResponseWriter, r *http.Request, user models.User) {
	id := r.PathValue("bookId")
	writeJSON(w, http.StatusOK, map[string]any{"kb_drift": map[string]any{"book_id": id, "has_drift": false}, "log_health": map[string]any{"book_id": id, "total_entries": 0, "error_entries": 0, "block_failures": 0}})
}
func (s *Server) dtBookRefreshFingerprints(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"book_id": r.PathValue("bookId"), "kb_fingerprints": map[string]string{}, "stale_page_ids": []string{}})
}
func (s *Server) dtLegacyChatSession(w http.ResponseWriter, r *http.Request, user models.User) {
	conversation, err := s.store.GetConversationByID(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	messages, _ := s.loadConversationMessages(r.Context(), user.ID, conversation)
	writeJSON(w, http.StatusOK, map[string]any{"session_id": conversation.ID, "messages": messages})
}

func userNotebooks(userID string) map[string]*dtNotebook {
	if dtState.notebooks[userID] == nil {
		dtState.notebooks[userID] = map[string]*dtNotebook{}
	}
	return dtState.notebooks[userID]
}
func userQuestionEntries(userID string) map[int]*dtQuestionEntry {
	if dtState.questionEntries[userID] == nil {
		dtState.questionEntries[userID] = map[int]*dtQuestionEntry{}
	}
	return dtState.questionEntries[userID]
}
func userQuestionCategories(userID string) map[int]*dtQuestionCategory {
	if dtState.questionCategories[userID] == nil {
		dtState.questionCategories[userID] = map[int]*dtQuestionCategory{}
	}
	return dtState.questionCategories[userID]
}
func userLearning(userID string) map[string]*dtLearningProgress {
	if dtState.learning[userID] == nil {
		dtState.learning[userID] = map[string]*dtLearningProgress{}
	}
	return dtState.learning[userID]
}
func userBooks(userID string) map[string]*dtBookBundle {
	if dtState.books[userID] == nil {
		dtState.books[userID] = map[string]*dtBookBundle{}
	}
	return dtState.books[userID]
}
func ensureLearning(userID, bookID string) *dtLearningProgress {
	if strings.TrimSpace(bookID) == "" {
		bookID = "default"
	}
	items := userLearning(userID)
	if items[bookID] == nil {
		items[bookID] = &dtLearningProgress{BookID: bookID, Modules: []dtLearningModule{}, MasteryLevels: map[string]int{}, CurrentStage: "new", UpdatedAt: time.Now().UnixMilli()}
	}
	return items[bookID]
}
