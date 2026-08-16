package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"be-miawai/internal/database"
	"be-miawai/internal/models"
)

var dtCatalogState = struct {
	sync.Mutex
	personas      map[string]map[string]*dtPersona
	skills        map[string]map[string]*dtSkill
	skillTags     map[string]map[string]bool
	partners      map[string]map[string]*dtPartner
	subagentConns map[string]map[string]dtSubagentConnection
	avatarMarkers map[string]string
}{
	personas:      map[string]map[string]*dtPersona{},
	skills:        map[string]map[string]*dtSkill{},
	skillTags:     map[string]map[string]bool{},
	partners:      map[string]map[string]*dtPartner{},
	subagentConns: map[string]map[string]dtSubagentConnection{},
	avatarMarkers: map[string]string{},
}

type dtProfile struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
	Disabled  bool   `json:"disabled,omitempty"`
	Avatar    string `json:"avatar,omitempty"`
}

type dtPersona struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	ReadOnly    bool   `json:"read_only"`
	Content     string `json:"content,omitempty"`
}

type dtSkill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Source      string   `json:"source,omitempty"`
	ReadOnly    bool     `json:"read_only,omitempty"`
	Content     string   `json:"content,omitempty"`
}

type dtPartner struct {
	PartnerID          string         `json:"partner_id"`
	Name               string         `json:"name"`
	Description        string         `json:"description"`
	Channels           any            `json:"channels"`
	LLMSelection       any            `json:"llm_selection,omitempty"`
	BackupLLMSelection any            `json:"backup_llm_selection,omitempty"`
	Model              *string        `json:"model,omitempty"`
	Language           string         `json:"language,omitempty"`
	Emoji              string         `json:"emoji,omitempty"`
	Color              string         `json:"color,omitempty"`
	Avatar             string         `json:"avatar,omitempty"`
	SoulOrigin         map[string]any `json:"soul_origin,omitempty"`
	EnabledTools       []string       `json:"enabled_tools,omitempty"`
	BuiltinTools       []string       `json:"builtin_tools,omitempty"`
	MCPTools           []string       `json:"mcp_tools,omitempty"`
	Running            bool           `json:"running"`
	StartedAt          *string        `json:"started_at"`
	LastReloadError    *string        `json:"last_reload_error,omitempty"`
	Soul               string         `json:"-"`
}

type dtSubagentConnection struct {
	Name        string  `json:"name"`
	AgentKind   string  `json:"agent_kind"`
	CWD         string  `json:"cwd"`
	PartnerID   string  `json:"partner_id,omitempty"`
	Description string  `json:"description,omitempty"`
	CreatedAt   string  `json:"created_at,omitempty"`
	UpdatedAt   *string `json:"updated_at,omitempty"`
}

func (s *Server) dtProfilePayload(user models.User) dtProfile {
	avatar := dtCatalogState.avatarMarkers[user.ID]
	return dtProfile{ID: user.ID, Username: firstNonEmpty(user.Email, user.Name), Role: firstNonEmpty(user.Role, "user"), CreatedAt: user.CreatedAt.UTC().Format(time.RFC3339), Avatar: avatar}
}

func (s *Server) dtGetProfile(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	profile := s.dtProfilePayload(user)
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, profile)
}

func (s *Server) dtUpdateProfile(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Avatar string `json:"avatar"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dtCatalogState.Lock()
	dtCatalogState.avatarMarkers[user.ID] = body.Avatar
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"avatar": body.Avatar})
}

func (s *Server) dtUploadAvatar(w http.ResponseWriter, r *http.Request, user models.User) {
	marker := "img:" + time.Now().UTC().Format("20060102150405")
	dtCatalogState.Lock()
	dtCatalogState.avatarMarkers[user.ID] = marker
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"avatar": marker})
}

func (s *Server) dtRemoveAvatar(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	delete(dtCatalogState.avatarMarkers, user.ID)
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
}

func (s *Server) dtAvatarImage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="96" height="96"><rect width="96" height="96" rx="24" fill="#7f3f2a"/><text x="48" y="56" text-anchor="middle" font-size="32" fill="#f8efe0">DT</text></svg>`))
}

func (s *Server) dtAdminListUsers(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, []dtProfile{s.dtProfilePayload(user)})
}

func (s *Server) dtAdminCreateUser(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct{ Username, Password string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	username := body.Username
	if username != "" && !strings.Contains(username, "@") {
		username += "@deeptutor.local"
	}
	password := body.Password
	if len(password) < 8 {
		password = password + "12345678"
	}
	r.Body = newJSONBody(map[string]string{"email": username, "name": strings.Split(username, "@")[0], "password": password})
	s.register(w, r)
}

func (s *Server) dtAdminNoop(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) dtListPersonas(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	defer dtCatalogState.Unlock()
	items := []dtPersona{}
	for _, p := range userPersonas(user.ID) {
		cp := *p
		cp.Content = ""
		items = append(items, cp)
	}
	writeJSON(w, http.StatusOK, map[string][]dtPersona{"personas": items})
}
func (s *Server) dtGetPersona(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	p := userPersonas(user.ID)[r.PathValue("name")]
	dtCatalogState.Unlock()
	if p == nil {
		writeError(w, http.StatusNotFound, "persona not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}
func (s *Server) dtCreatePersona(w http.ResponseWriter, r *http.Request, user models.User) {
	var body dtPersona
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	body.Source = "user"
	dtCatalogState.Lock()
	userPersonas(user.ID)[body.Name] = &body
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, body)
}
func (s *Server) dtUpdatePersona(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Description, Content, RenameTo string `json:",omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := r.PathValue("name")
	dtCatalogState.Lock()
	p := userPersonas(user.ID)[name]
	if p != nil {
		if body.Description != "" {
			p.Description = body.Description
		}
		if body.Content != "" {
			p.Content = body.Content
		}
		if body.RenameTo != "" {
			delete(userPersonas(user.ID), name)
			p.Name = body.RenameTo
			userPersonas(user.ID)[p.Name] = p
		}
	}
	dtCatalogState.Unlock()
	if p == nil {
		writeError(w, http.StatusNotFound, "persona not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}
func (s *Server) dtDeletePersona(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	delete(userPersonas(user.ID), r.PathValue("name"))
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) dtListSkills(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	defer dtCatalogState.Unlock()
	items := []dtSkill{}
	for _, sk := range userSkills(user.ID) {
		cp := *sk
		cp.Content = ""
		items = append(items, cp)
	}
	writeJSON(w, http.StatusOK, map[string][]dtSkill{"skills": items})
}
func (s *Server) dtGetSkill(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	sk := userSkills(user.ID)[r.PathValue("name")]
	dtCatalogState.Unlock()
	if sk == nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}
	writeJSON(w, http.StatusOK, sk)
}
func (s *Server) dtCreateSkill(w http.ResponseWriter, r *http.Request, user models.User) {
	var body dtSkill
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	body.Source = "user"
	dtCatalogState.Lock()
	userSkills(user.ID)[body.Name] = &body
	for _, t := range body.Tags {
		userSkillTags(user.ID)[strings.ToLower(strings.TrimSpace(t))] = true
	}
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, body)
}
func (s *Server) dtUpdateSkill(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Description string   `json:"description"`
		Content     string   `json:"content"`
		RenameTo    string   `json:"rename_to"`
		Tags        []string `json:"tags"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := r.PathValue("name")
	dtCatalogState.Lock()
	sk := userSkills(user.ID)[name]
	if sk != nil {
		if body.Description != "" {
			sk.Description = body.Description
		}
		if body.Content != "" {
			sk.Content = body.Content
		}
		if body.Tags != nil {
			sk.Tags = body.Tags
		}
		if body.RenameTo != "" {
			delete(userSkills(user.ID), name)
			sk.Name = body.RenameTo
			userSkills(user.ID)[sk.Name] = sk
		}
	}
	dtCatalogState.Unlock()
	if sk == nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}
	writeJSON(w, http.StatusOK, sk)
}
func (s *Server) dtDeleteSkill(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	delete(userSkills(user.ID), r.PathValue("name"))
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
func (s *Server) dtInstallSkill(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"skill": map[string]string{"name": "installed"}, "version": "local", "verdict": map[string]string{"status": "trusted", "detail": "Installed locally"}})
}
func (s *Server) dtHubCatalog(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"hub": "eduhub", "web_url": "", "skills": []any{}})
}
func (s *Server) dtHubDetail(w http.ResponseWriter, r *http.Request, user models.User) {
	slug := r.URL.Query().Get("slug")
	writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "name": slug, "summary": "", "version": "", "downloads": 0, "stars": 0, "owner": "", "owner_url": "", "content": "", "tags": []string{}, "web_url": ""})
}
func (s *Server) dtListSkillTags(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	defer dtCatalogState.Unlock()
	tags := []string{}
	for t := range userSkillTags(user.ID) {
		tags = append(tags, t)
	}
	writeJSON(w, http.StatusOK, map[string][]string{"tags": tags})
}
func (s *Server) dtCreateSkillTag(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := strings.ToLower(strings.TrimSpace(body.Name))
	dtCatalogState.Lock()
	userSkillTags(user.ID)[name] = true
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}
func (s *Server) dtRenameSkillTag(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		RenameTo string `json:"rename_to"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	next := strings.ToLower(strings.TrimSpace(body.RenameTo))
	dtCatalogState.Lock()
	delete(userSkillTags(user.ID), r.PathValue("name"))
	userSkillTags(user.ID)[next] = true
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"name": next})
}
func (s *Server) dtDeleteSkillTag(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	delete(userSkillTags(user.ID), r.PathValue("name"))
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) dtListPartners(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	defer dtCatalogState.Unlock()
	items := []dtPartner{}
	for _, p := range userPartners(user.ID) {
		items = append(items, *p)
	}
	writeJSON(w, http.StatusOK, items)
}
func (s *Server) dtGetPartner(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	p := userPartners(user.ID)[r.PathValue("partnerId")]
	dtCatalogState.Unlock()
	if p == nil {
		writeError(w, http.StatusNotFound, "partner not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}
func (s *Server) dtCreatePartner(w http.ResponseWriter, r *http.Request, user models.User) {
	var body dtPartner
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.PartnerID == "" {
		body.PartnerID = database.NewID("partner")
	}
	if body.Channels == nil {
		body.Channels = []string{}
	}
	if body.Language == "" {
		body.Language = "id"
	}
	dtCatalogState.Lock()
	userPartners(user.ID)[body.PartnerID] = &body
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, body)
}
func (s *Server) dtUpdatePartner(w http.ResponseWriter, r *http.Request, user models.User) {
	var body dtPartner
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := r.PathValue("partnerId")
	dtCatalogState.Lock()
	p := userPartners(user.ID)[id]
	if p != nil {
		if body.Name != "" {
			p.Name = body.Name
		}
		if body.Description != "" {
			p.Description = body.Description
		}
		if body.Channels != nil {
			p.Channels = body.Channels
		}
		if body.Language != "" {
			p.Language = body.Language
		}
		if body.Emoji != "" {
			p.Emoji = body.Emoji
		}
		if body.Color != "" {
			p.Color = body.Color
		}
		if body.Avatar != "" {
			p.Avatar = body.Avatar
		}
	}
	dtCatalogState.Unlock()
	if p == nil {
		writeError(w, http.StatusNotFound, "partner not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}
func (s *Server) dtStartPartner(w http.ResponseWriter, r *http.Request, user models.User) {
	s.dtSetPartnerRunning(w, r, user, true)
}
func (s *Server) dtStopPartner(w http.ResponseWriter, r *http.Request, user models.User) {
	s.dtSetPartnerRunning(w, r, user, false)
}
func (s *Server) dtSetPartnerRunning(w http.ResponseWriter, r *http.Request, user models.User, running bool) {
	id := r.PathValue("partnerId")
	now := time.Now().Format(time.RFC3339)
	dtCatalogState.Lock()
	p := userPartners(user.ID)[id]
	if p != nil {
		p.Running = running
		if running {
			p.StartedAt = &now
		} else {
			p.StartedAt = nil
		}
	}
	dtCatalogState.Unlock()
	if p == nil {
		writeError(w, http.StatusNotFound, "partner not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}
func (s *Server) dtDeletePartner(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	delete(userPartners(user.ID), r.PathValue("partnerId"))
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
func (s *Server) dtPartnerSoul(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	p := userPartners(user.ID)[r.PathValue("partnerId")]
	dtCatalogState.Unlock()
	if p == nil {
		writeError(w, http.StatusNotFound, "partner not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": p.Soul})
}
func (s *Server) dtSavePartnerSoul(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Content string `json:"content"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dtCatalogState.Lock()
	p := userPartners(user.ID)[r.PathValue("partnerId")]
	if p != nil {
		p.Soul = body.Content
	}
	dtCatalogState.Unlock()
	if p == nil {
		writeError(w, http.StatusNotFound, "partner not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Server) dtPartnerSources(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"library": []any{}, "personas": []any{}})
}
func (s *Server) dtCreateSoul(w http.ResponseWriter, r *http.Request, user models.User) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusOK, body)
}
func (s *Server) dtPartnerToolOptions(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"tools": []any{}, "builtin_tools": []any{}, "mcp_tools": []any{}})
}
func (s *Server) dtPartnerCommands(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string][]any{"commands": []any{}})
}
func (s *Server) dtPartnerAssets(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"knowledge_bases": []any{}, "skills": []any{}, "notebooks": []any{}})
}
func (s *Server) dtAddPartnerAssets(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"assets": map[string]any{"knowledge_bases": []any{}, "skills": []any{}, "notebooks": []any{}}, "copied": map[string][]string{}, "errors": []any{}})
}
func (s *Server) dtRemovePartnerAsset(w http.ResponseWriter, r *http.Request, user models.User) {
	s.dtPartnerAssets(w, r, user)
}
func (s *Server) dtChannelSchemas(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"channels": map[string]any{}})
}
func (s *Server) dtPartnerHistory(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, []any{})
}
func (s *Server) dtPartnerSessions(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, []any{})
}
func (s *Server) dtPartnerSessionAction(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Server) dtPartnerBranch(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"session": map[string]any{"session_key": "branch", "message_count": 0, "updated_at": time.Now().Format(time.RFC3339), "last_message": ""}})
}

func (s *Server) dtSubagentPartners(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	defer dtCatalogState.Unlock()
	partners := []map[string]any{}
	for _, p := range userPartners(user.ID) {
		partners = append(partners, map[string]any{"partner_id": p.PartnerID, "name": p.Name, "description": p.Description, "emoji": p.Emoji, "color": p.Color, "avatar": p.Avatar, "language": p.Language, "running": p.Running})
	}
	writeJSON(w, http.StatusOK, map[string]any{"partners": partners})
}
func (s *Server) dtSubagentDetect(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"backends": []map[string]any{{"kind": "partner", "display_name": "Partner", "available": true, "version": "local", "detail": "Miaw compatibility backend"}}})
}
func (s *Server) dtSubagentConnections(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	defer dtCatalogState.Unlock()
	items := []dtSubagentConnection{}
	for _, c := range userSubagentConns(user.ID) {
		items = append(items, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": items})
}
func (s *Server) dtSubagentConnect(w http.ResponseWriter, r *http.Request, user models.User) {
	var body dtSubagentConnection
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.Name == "" {
		body.Name = database.NewID("subagent")
	}
	body.CreatedAt = time.Now().Format(time.RFC3339)
	dtCatalogState.Lock()
	userSubagentConns(user.ID)[body.Name] = body
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, body)
}
func (s *Server) dtSubagentDisconnect(w http.ResponseWriter, r *http.Request, user models.User) {
	dtCatalogState.Lock()
	delete(userSubagentConns(user.ID), r.PathValue("name"))
	dtCatalogState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
func (s *Server) dtSubagentBackendOptions(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"backends": []map[string]any{{"kind": "partner", "display_name": "Partner", "available": true, "version": "local", "default_model": "default", "models": []any{}, "efforts": []string{"medium"}, "allow_custom_model": true, "synced_at": time.Now().Format(time.RFC3339), "detail": "Miaw compatibility backend"}}})
}

func (s *Server) dtSubagentSettings(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"consult_budget": 3, "backends": map[string]any{}})
}

func (s *Server) dtSubagentMessage(w http.ResponseWriter, r *http.Request, user models.User) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{\"channel\":\"assistant\",\"text\":\"Subagent compatibility mode is active.\"}\n"))
	_, _ = w.Write([]byte("{\"done\":true,\"success\":true}\n"))
}

func userPersonas(userID string) map[string]*dtPersona {
	if dtCatalogState.personas[userID] == nil {
		dtCatalogState.personas[userID] = map[string]*dtPersona{}
	}
	return dtCatalogState.personas[userID]
}
func userSkills(userID string) map[string]*dtSkill {
	if dtCatalogState.skills[userID] == nil {
		dtCatalogState.skills[userID] = map[string]*dtSkill{}
	}
	return dtCatalogState.skills[userID]
}
func userSkillTags(userID string) map[string]bool {
	if dtCatalogState.skillTags[userID] == nil {
		dtCatalogState.skillTags[userID] = map[string]bool{}
	}
	return dtCatalogState.skillTags[userID]
}
func userPartners(userID string) map[string]*dtPartner {
	if dtCatalogState.partners[userID] == nil {
		dtCatalogState.partners[userID] = map[string]*dtPartner{}
	}
	return dtCatalogState.partners[userID]
}
func userSubagentConns(userID string) map[string]dtSubagentConnection {
	if dtCatalogState.subagentConns[userID] == nil {
		dtCatalogState.subagentConns[userID] = map[string]dtSubagentConnection{}
	}
	return dtCatalogState.subagentConns[userID]
}
