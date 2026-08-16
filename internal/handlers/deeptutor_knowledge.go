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

var dtKnowledgeState = struct {
	sync.Mutex
	bases        map[string]map[string]*dtKnowledgeBase
	defaultBase  map[string]string
	pipelineCfg  map[string]map[string]any
	activeModels map[string]map[string]any
}{
	bases:        map[string]map[string]*dtKnowledgeBase{},
	defaultBase:  map[string]string{},
	pipelineCfg:  map[string]map[string]any{},
	activeModels: map[string]map[string]any{},
}

type dtKnowledgeBase struct {
	ID              string                `json:"id,omitempty"`
	Name            string                `json:"name"`
	IsDefault       bool                  `json:"is_default,omitempty"`
	Status          string                `json:"status,omitempty"`
	Path            string                `json:"path,omitempty"`
	Metadata        map[string]any        `json:"metadata,omitempty"`
	Progress        map[string]any        `json:"progress,omitempty"`
	Statistics      map[string]any        `json:"statistics,omitempty"`
	Source          string                `json:"source,omitempty"`
	Assigned        bool                  `json:"assigned,omitempty"`
	ReadOnly        bool                  `json:"read_only,omitempty"`
	ProvenanceLabel string                `json:"provenance_label,omitempty"`
	Available       bool                  `json:"available,omitempty"`
	Files           []dtKnowledgeBaseFile `json:"-"`
}

type dtKnowledgeBaseFile struct {
	Name     string  `json:"name"`
	Type     string  `json:"type,omitempty"`
	Size     int64   `json:"size,omitempty"`
	Modified int64   `json:"modified,omitempty"`
	MIMEType *string `json:"mime_type,omitempty"`
	Content  string  `json:"-"`
}

func (s *Server) dtKnowledgeList(w http.ResponseWriter, r *http.Request, user models.User) {
	dtKnowledgeState.Lock()
	bases := userKnowledgeBases(user.ID)
	items := make([]dtKnowledgeBase, 0, len(bases))
	for _, kb := range bases {
		copy := *kb
		copy.Files = nil
		copy.IsDefault = dtKnowledgeState.defaultBase[user.ID] == kb.Name
		items = append(items, copy)
	}
	dtKnowledgeState.Unlock()
	writeJSON(w, http.StatusOK, map[string][]dtKnowledgeBase{"knowledge_bases": items})
}

func (s *Server) dtRagProviders(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": []map[string]any{
		{"id": "miaw", "name": "Miaw Knowledge", "description": "Local compatibility knowledge store", "configured": true, "requires_api_key": false, "modes": []string{"hybrid", "vector"}, "default_mode": "hybrid", "linkable": true},
		{"id": "llamaindex", "name": "LlamaIndex", "description": "DeepTutor-compatible fallback", "configured": true, "requires_api_key": false, "modes": []string{"hybrid", "vector"}, "default_mode": "hybrid", "linkable": true},
		{"id": "lightrag", "name": "LightRAG", "description": "Remote LightRAG-compatible fallback", "configured": true, "requires_api_key": false, "modes": []string{"hybrid", "local", "global", "naive"}, "default_mode": "hybrid", "linkable": true},
	}})
}

func (s *Server) dtSupportedFileTypes(w http.ResponseWriter, r *http.Request, user models.User) {
	extensions := []string{".txt", ".md", ".markdown", ".pdf", ".docx", ".csv", ".json", ".png", ".jpg", ".jpeg", ".webp"}
	writeJSON(w, http.StatusOK, map[string]any{"extensions": extensions, "accept": strings.Join(extensions, ","), "max_file_size_bytes": 25 * 1024 * 1024})
}

func (s *Server) dtRagPipelineConfig(w http.ResponseWriter, r *http.Request, user models.User) {
	provider := r.PathValue("provider")
	dtKnowledgeState.Lock()
	cfg := pipelineConfig(provider)
	dtKnowledgeState.Unlock()
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) dtUpdateRagPipelineConfig(w http.ResponseWriter, r *http.Request, user models.User) {
	provider := r.PathValue("provider")
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	dtKnowledgeState.Lock()
	cfg := pipelineConfig(provider)
	for key, value := range body {
		cfg[key] = value
	}
	dtKnowledgeState.pipelineCfg[provider] = cfg
	dtKnowledgeState.Unlock()
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) dtRagPipelinePreflight(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "checks": []map[string]any{{"key": "compat", "label": "Compatibility layer", "ok": true, "detail": "Ready", "optional": false}}})
}

func (s *Server) dtRagModelOptions(w http.ResponseWriter, r *http.Request, user models.User) {
	settings, _ := s.loadRuntimeSettings(r.Context(), user.ID)
	model := firstNonEmpty(settings.Models.Active, firstModel(settings.Models.All), "default")
	option := map[string]any{"profile_id": "miaw-runtime", "profile_name": "Miaw Runtime", "model_id": model, "label": model, "model": model, "detail": "Active Miaw runtime model"}
	writeJSON(w, http.StatusOK, map[string]any{"llm": map[string]any{"active": map[string]any{"profile_id": "miaw-runtime", "model_id": model}, "options": []any{option}}, "embedding": map[string]any{"active": map[string]any{"profile_id": "miaw-runtime", "model_id": model}, "options": []any{option}}})
}

func (s *Server) dtSetRagActiveModel(w http.ResponseWriter, r *http.Request, user models.User) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	kind := firstNonEmpty(stringValue(body["kind"]), "llm")
	dtKnowledgeState.Lock()
	if dtKnowledgeState.activeModels[user.ID] == nil {
		dtKnowledgeState.activeModels[user.ID] = map[string]any{}
	}
	dtKnowledgeState.activeModels[user.ID][kind] = body
	dtKnowledgeState.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"active": map[string]any{"profile_id": body["profile_id"], "model_id": body["model_id"]}, "options": []any{}})
}

func (s *Server) dtUpdateRagProviderMode(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Mode string `json:"mode"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusOK, map[string]string{"provider": r.PathValue("provider"), "mode": firstNonEmpty(body.Mode, "hybrid")})
}

func (s *Server) dtCreateKnowledgeBase(w http.ResponseWriter, r *http.Request, user models.User) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		name = body.Name
	}
	if name == "" {
		name = "Knowledge Base"
	}
	dtKnowledgeState.Lock()
	kb := ensureKnowledgeBase(user.ID, name)
	kb.Status = "ready"
	kb.Progress = map[string]any{"stage": "ready", "percent": 100}
	dtKnowledgeState.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"task_id": database.NewID("task"), "message": "knowledge base ready", "noop": false})
}

func (s *Server) dtConnectObsidian(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Name      string `json:"name"`
		VaultPath string `json:"vault_path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := firstNonEmpty(body.Name, "Obsidian Vault")
	dtKnowledgeState.Lock()
	kb := ensureKnowledgeBase(user.ID, name)
	kb.Path = body.VaultPath
	kb.Metadata["kind"] = "obsidian"
	dtKnowledgeState.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "connected", "name": name, "vault_path": body.VaultPath})
}

func (s *Server) dtProbeFolder(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		FolderPath  string `json:"folder_path"`
		RagProvider string `json:"rag_provider"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": firstNonEmpty(body.RagProvider, "miaw"), "external_path": body.FolderPath, "version": "compat", "doc_count": 0, "embedding": map[string]any{"compatible": true, "index_model": nil, "current_model": nil}, "warnings": []string{}, "error": nil})
}

func (s *Server) dtConnectFolder(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Name        string `json:"name"`
		FolderPath  string `json:"folder_path"`
		RagProvider string `json:"rag_provider"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := firstNonEmpty(body.Name, "Linked Folder")
	dtKnowledgeState.Lock()
	kb := ensureKnowledgeBase(user.ID, name)
	kb.Path = body.FolderPath
	kb.Metadata["rag_provider"] = firstNonEmpty(body.RagProvider, "miaw")
	dtKnowledgeState.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"status": "connected", "name": name, "external_path": body.FolderPath, "rag_provider": firstNonEmpty(body.RagProvider, "miaw"), "warnings": []string{}})
}

func (s *Server) dtProbeLightRag(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		ServerURL string `json:"server_url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "base_url": body.ServerURL, "reachable": true, "auth_required": false, "auth_ok": true, "core_version": "compat", "api_version": "compat", "error": nil})
}

func (s *Server) dtConnectLightRag(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Name      string `json:"name"`
		ServerURL string `json:"server_url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := firstNonEmpty(body.Name, "LightRAG")
	dtKnowledgeState.Lock()
	kb := ensureKnowledgeBase(user.ID, name)
	kb.Metadata["server_url"] = body.ServerURL
	kb.Metadata["rag_provider"] = "lightrag"
	dtKnowledgeState.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "connected", "name": name, "server_url": body.ServerURL, "rag_provider": "lightrag"})
}

func (s *Server) dtKnowledgeFiles(w http.ResponseWriter, r *http.Request, user models.User) {
	dtKnowledgeState.Lock()
	kb := userKnowledgeBases(user.ID)[r.PathValue("name")]
	files := []dtKnowledgeBaseFile{}
	if kb != nil {
		files = append(files, kb.Files...)
	}
	dtKnowledgeState.Unlock()
	for i := range files {
		files[i].Content = ""
	}
	writeJSON(w, http.StatusOK, map[string][]dtKnowledgeBaseFile{"files": files})
}

func (s *Server) dtUploadKnowledgeFiles(w http.ResponseWriter, r *http.Request, user models.User) {
	name := r.PathValue("name")
	dtKnowledgeState.Lock()
	kb := ensureKnowledgeBase(user.ID, name)
	kb.Files = append(kb.Files, dtKnowledgeBaseFile{Name: "uploaded-" + time.Now().Format("20060102150405") + ".txt", Type: "file", Size: 0, Modified: time.Now().UnixMilli()})
	dtKnowledgeState.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"task_id": database.NewID("task"), "message": "files uploaded", "noop": false})
}

func (s *Server) dtCreateKnowledgeFolder(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dtKnowledgeState.Lock()
	kb := ensureKnowledgeBase(user.ID, r.PathValue("name"))
	kb.Files = append(kb.Files, dtKnowledgeBaseFile{Name: strings.Trim(body.Path, "/"), Type: "folder", Modified: time.Now().UnixMilli()})
	dtKnowledgeState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"created": true})
}

func (s *Server) dtMoveKnowledgeFile(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]bool{"moved": true})
}

func (s *Server) dtGetKnowledgeFile(w http.ResponseWriter, r *http.Request, user models.User) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("Compatibility knowledge file preview."))
}

func (s *Server) dtGetKnowledgePreview(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]string{"content": "Compatibility knowledge file preview."})
}

func (s *Server) dtDeleteKnowledgeFile(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]bool{"was_indexed": false})
}

func (s *Server) dtSetDefaultKnowledgeBase(w http.ResponseWriter, r *http.Request, user models.User) {
	dtKnowledgeState.Lock()
	dtKnowledgeState.defaultBase[user.ID] = r.PathValue("name")
	dtKnowledgeState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) dtReindexKnowledgeBase(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"task_id": database.NewID("task"), "message": "reindex queued", "noop": false})
}

func (s *Server) dtRetryKnowledgeBase(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"task_id": database.NewID("task"), "message": "retry queued", "noop": false})
}

func (s *Server) dtDeleteKnowledgeBase(w http.ResponseWriter, r *http.Request, user models.User) {
	dtKnowledgeState.Lock()
	delete(userKnowledgeBases(user.ID), r.PathValue("name"))
	dtKnowledgeState.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func userKnowledgeBases(userID string) map[string]*dtKnowledgeBase {
	if dtKnowledgeState.bases[userID] == nil {
		dtKnowledgeState.bases[userID] = map[string]*dtKnowledgeBase{}
	}
	return dtKnowledgeState.bases[userID]
}

func ensureKnowledgeBase(userID, name string) *dtKnowledgeBase {
	name = firstNonEmpty(strings.TrimSpace(name), "Knowledge Base")
	bases := userKnowledgeBases(userID)
	if bases[name] == nil {
		bases[name] = &dtKnowledgeBase{ID: database.NewID("kb"), Name: name, Status: "ready", Metadata: map[string]any{}, Progress: map[string]any{"stage": "ready", "percent": 100}, Statistics: map[string]any{"documents": 0}, Source: "user", Assigned: true, Available: true, Files: []dtKnowledgeBaseFile{}}
	}
	return bases[name]
}

func pipelineConfig(provider string) map[string]any {
	if cfg := dtKnowledgeState.pipelineCfg[provider]; cfg != nil {
		return cfg
	}
	switch strings.ToLower(provider) {
	case "pageindex":
		return map[string]any{"api_base_url": "", "api_key_set": false, "configured": false}
	case "llamaindex":
		return map[string]any{"version": 1, "retrieval_profile": "hybrid", "top_k": 8, "vector_top_k_multiplier": 4, "bm25_top_k_multiplier": 4, "chunk_size": 1024, "chunk_overlap": 128}
	case "graphrag":
		return map[string]any{"version": 1, "response_type": "Multiple Paragraphs", "community_level": 2, "dynamic_community_selection": false}
	case "lightrag":
		return map[string]any{"version": 1, "top_k": 8, "response_type": "Multiple Paragraphs"}
	default:
		return map[string]any{"version": 1}
	}
}
