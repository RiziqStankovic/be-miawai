package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"be-miawai/internal/models"

	"github.com/coder/websocket"
)

var dtSpaceState = struct {
	sync.Mutex
	adminMCP map[string]map[string]any
	userMCP  map[string]map[string]map[string]any
	cliApps  map[string]map[string]dtCliApp
}{
	adminMCP: map[string]map[string]any{},
	userMCP:  map[string]map[string]map[string]any{},
	cliApps:  map[string]map[string]dtCliApp{},
}

type dtCliApp struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	ToolName    string `json:"tool_name"`
	EntryPoint  string `json:"entry_point"`
	Runtime     string `json:"runtime"`
	InstalledAt string `json:"installed_at"`
	Version     string `json:"version"`
	Pin         string `json:"pin"`
	Trust       string `json:"trust"`
	Granted     bool   `json:"granted"`
	Enabled     bool   `json:"enabled"`
	InCatalog   bool   `json:"in_catalog"`
}

func (s *Server) dtTools(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled_optional_tools": []string{"web_search", "memory", "knowledge"},
		"available_optional_tools": []map[string]string{
			{"name": "web_search", "description": "Search the web when research is needed"},
			{"name": "memory", "description": "Use saved user memories"},
			{"name": "knowledge", "description": "Use connected knowledge bases"},
		},
	})
}

func (s *Server) dtGetMCPSettings(w http.ResponseWriter, r *http.Request, user models.User) {
	dtSpaceState.Lock()
	servers := cloneAnyMap(dtSpaceState.adminMCP)
	dtSpaceState.Unlock()
	writeJSON(w, http.StatusOK, mcpSettingsPayload(servers, false))
}

func (s *Server) dtUpdateMCPSettings(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Servers map[string]map[string]any `json:"servers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.Servers == nil {
		body.Servers = map[string]map[string]any{}
	}
	dtSpaceState.Lock()
	dtSpaceState.adminMCP = body.Servers
	dtSpaceState.Unlock()
	writeJSON(w, http.StatusOK, mcpSettingsPayload(body.Servers, false))
}

func (s *Server) dtTestMCP(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tools": []any{}, "error": ""})
}

func (s *Server) dtSpaceMCPServers(w http.ResponseWriter, r *http.Request, user models.User) {
	dtSpaceState.Lock()
	servers := cloneAnyMap(userMCPServers(user.ID))
	dtSpaceState.Unlock()
	writeJSON(w, http.StatusOK, mcpSettingsPayload(servers, true))
}

func (s *Server) dtPutSpaceMCPServer(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Config  map[string]any    `json:"config"`
		Secrets map[string]string `json:"secrets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "server name is required")
		return
	}
	if body.Config == nil {
		body.Config = map[string]any{}
	}
	dtSpaceState.Lock()
	servers := userMCPServers(user.ID)
	servers[name] = body.Config
	payload := mcpSettingsPayload(cloneAnyMap(servers), true)
	dtSpaceState.Unlock()
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) dtDeleteSpaceMCPServer(w http.ResponseWriter, r *http.Request, user models.User) {
	dtSpaceState.Lock()
	servers := userMCPServers(user.ID)
	delete(servers, r.PathValue("name"))
	payload := mcpSettingsPayload(cloneAnyMap(servers), true)
	dtSpaceState.Unlock()
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) dtAuthorizeSpaceMCPServer(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]string{"authorize_url": "/settings/mcp?authorized=1"})
}

func (s *Server) dtMCPCatalog(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":     []any{},
		"next_cursor": "",
		"total":       0,
		"categories":  map[string]int{},
	})
}

func (s *Server) dtInstallMCPCatalog(w http.ResponseWriter, r *http.Request, user models.User) {
	name := firstNonEmpty(r.PathValue("entryId"), "mcp_server")
	dtSpaceState.Lock()
	servers := userMCPServers(user.ID)
	servers[name] = map[string]any{"type": "streamableHttp", "url": "", "enabled": true, "enabled_tools": []string{"*"}, "catalog_entry": r.PathValue("entryId")}
	payload := mcpSettingsPayload(cloneAnyMap(servers), true)
	dtSpaceState.Unlock()
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) dtCliApps(w http.ResponseWriter, r *http.Request, user models.User) {
	dtSpaceState.Lock()
	apps := userCliApps(user.ID)
	list := make([]dtCliApp, 0, len(apps))
	for _, app := range apps {
		list = append(list, app)
	}
	dtSpaceState.Unlock()
	writeJSON(w, http.StatusOK, cliAppsPayload(list, ""))
}

func (s *Server) dtCliAppEnabled(w http.ResponseWriter, r *http.Request, user models.User) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dtSpaceState.Lock()
	apps := userCliApps(user.ID)
	app := apps[r.PathValue("appId")]
	if app.ID == "" {
		app = newCliApp(r.PathValue("appId"))
	}
	app.Enabled = body.Enabled
	app.Granted = true
	apps[app.ID] = app
	list := make([]dtCliApp, 0, len(apps))
	for _, item := range apps {
		list = append(list, item)
	}
	dtSpaceState.Unlock()
	writeJSON(w, http.StatusOK, cliAppsPayload(list, ""))
}

func (s *Server) dtCliCatalog(w http.ResponseWriter, r *http.Request, user models.User) {
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":     []any{},
		"next_cursor": "",
		"total":       0,
		"categories":  map[string]int{},
		"catalog_pin": "local",
	})
}

func (s *Server) dtInstallCliApp(w http.ResponseWriter, r *http.Request, user models.User) {
	app := newCliApp(r.PathValue("appId"))
	dtSpaceState.Lock()
	apps := userCliApps(user.ID)
	apps[app.ID] = app
	list := make([]dtCliApp, 0, len(apps))
	for _, item := range apps {
		list = append(list, item)
	}
	dtSpaceState.Unlock()
	writeJSON(w, http.StatusOK, cliAppsPayload(list, "Installed in compatibility mode"))
}

func (s *Server) dtUninstallCliApp(w http.ResponseWriter, r *http.Request, user models.User) {
	dtSpaceState.Lock()
	apps := userCliApps(user.ID)
	delete(apps, r.PathValue("appId"))
	list := make([]dtCliApp, 0, len(apps))
	for _, item := range apps {
		list = append(list, item)
	}
	dtSpaceState.Unlock()
	writeJSON(w, http.StatusOK, cliAppsPayload(list, ""))
}

func (s *Server) dtUnifiedWS(w http.ResponseWriter, r *http.Request, user models.User) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	_ = wsWriteJSON(ctx, conn, map[string]any{"type": "connected", "status": "ready"})
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg map[string]any
		_ = json.Unmarshal(data, &msg)
		_ = wsWriteJSON(ctx, conn, map[string]any{"type": "ack", "echo_type": msg["type"], "created_at": time.Now().UnixMilli()})
	}
}

func (s *Server) dtBookWS(w http.ResponseWriter, r *http.Request, user models.User) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg map[string]any
		_ = json.Unmarshal(data, &msg)
		typeName := strings.TrimSpace(stringValue(msg["type"]))
		switch typeName {
		case "create":
			book := map[string]any{"id": "book_compat", "title": firstNonEmpty(stringValue(msg["user_intent"]), "Untitled Book"), "description": "Compatibility book", "status": "draft", "proposal": map[string]any{}, "knowledge_bases": []any{}, "language": "id", "page_count": 0, "chapter_count": 0, "created_at": time.Now().UnixMilli(), "updated_at": time.Now().UnixMilli(), "metadata": map[string]any{}}
			_ = wsWriteJSON(ctx, conn, map[string]any{"type": "create_result", "book": book, "proposal": map[string]any{"title": book["title"], "description": book["description"], "scope": "compatibility", "target_level": "general", "estimated_chapters": 1, "rationale": "Generated by Miaw compatibility layer"}})
		case "confirm_proposal":
			_ = wsWriteJSON(ctx, conn, map[string]any{"type": "confirm_proposal_result", "book": map[string]any{"id": msg["book_id"], "title": "Compatibility Book", "description": "", "status": "spine_ready", "proposal": map[string]any{}, "knowledge_bases": []any{}, "language": "id", "page_count": 0, "chapter_count": 0, "created_at": time.Now().UnixMilli(), "updated_at": time.Now().UnixMilli(), "metadata": map[string]any{}}, "spine": map[string]any{"book_id": msg["book_id"], "chapters": []any{}, "version": 1, "updated_at": time.Now().UnixMilli()}})
		case "compile_page":
			_ = wsWriteJSON(ctx, conn, map[string]any{"type": "compile_page_result", "page": map[string]any{"id": msg["page_id"], "book_id": msg["book_id"], "title": "Compatibility Page", "status": "ready", "blocks": []any{}, "links": []any{}, "created_at": time.Now().UnixMilli(), "updated_at": time.Now().UnixMilli()}})
		case "regenerate_block":
			_ = wsWriteJSON(ctx, conn, map[string]any{"type": "regenerate_block_result", "block": nil})
		default:
			_ = wsWriteJSON(ctx, conn, map[string]any{"type": "error", "error": "unsupported book ws event"})
		}
	}
}

func (s *Server) dtQuestionJudgeWS(w http.ResponseWriter, r *http.Request, user models.User) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	_ = wsWriteJSON(ctx, conn, map[string]any{"type": "delta", "content": "Compatibility judgment: answer received."})
	_ = wsWriteJSON(ctx, conn, map[string]any{"type": "done"})
}

func mcpSettingsPayload(servers map[string]map[string]any, includeUser bool) map[string]any {
	status := make([]map[string]any, 0, len(servers))
	for name, cfg := range servers {
		transport := firstNonEmpty(stringValue(cfg["type"]), "streamableHttp")
		state := "connected"
		if enabled, ok := cfg["enabled"].(bool); ok && !enabled {
			state = "disabled"
		}
		status = append(status, map[string]any{"name": name, "transport": transport, "status": state, "error": "", "tools": []any{}})
	}
	payload := map[string]any{"servers": servers, "status": status}
	if includeUser {
		payload["configured_secrets"] = map[string][]string{}
		payload["rejected"] = []any{}
		payload["deployment"] = map[string]any{"servers": []string{}, "status": []any{}}
		payload["limits"] = map[string]int{"max_servers": 0}
		payload["oauth"] = map[string]any{}
	}
	return payload
}

func cliAppsPayload(apps []dtCliApp, logText string) map[string]any {
	payload := map[string]any{"apps": apps, "access": map[string]bool{"unrestricted": true, "exec_denied": false}, "catalog_pin": "local"}
	if logText != "" {
		payload["log"] = logText
	}
	return payload
}

func newCliApp(id string) dtCliApp {
	id = firstNonEmpty(strings.TrimSpace(id), "cli_compat")
	return dtCliApp{ID: id, DisplayName: id, Description: "Compatibility CLI app", Category: "general", ToolName: "cli_" + strings.ReplaceAll(id, "-", "_"), EntryPoint: id, Runtime: "none", InstalledAt: time.Now().Format(time.RFC3339), Version: "local", Pin: "", Trust: "first-party", Granted: true, Enabled: true, InCatalog: true}
}

func userMCPServers(userID string) map[string]map[string]any {
	if dtSpaceState.userMCP[userID] == nil {
		dtSpaceState.userMCP[userID] = map[string]map[string]any{}
	}
	return dtSpaceState.userMCP[userID]
}

func userCliApps(userID string) map[string]dtCliApp {
	if dtSpaceState.cliApps[userID] == nil {
		dtSpaceState.cliApps[userID] = map[string]dtCliApp{}
	}
	return dtSpaceState.cliApps[userID]
}

func cloneAnyMap(input map[string]map[string]any) map[string]map[string]any {
	out := make(map[string]map[string]any, len(input))
	for key, value := range input {
		copy := make(map[string]any, len(value))
		for k, v := range value {
			copy[k] = v
		}
		out[key] = copy
	}
	return out
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func wsWriteJSON(ctx context.Context, conn *websocket.Conn, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}
