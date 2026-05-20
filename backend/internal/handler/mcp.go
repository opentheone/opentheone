package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/mcp"
	"github.com/opentheone/opentheone/backend/internal/model"
)

type MCPHandler struct {
	db  *gorm.DB
	mgr *mcp.Manager
}

func NewMCPHandler(db *gorm.DB, mgr *mcp.Manager) *MCPHandler {
	return &MCPHandler{db: db, mgr: mgr}
}

// validTransport canonicalizes the transport string, defaulting to stdio.
func validTransport(t string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "", "stdio":
		return "stdio", true
	case "streamable_http", "http", "https":
		return "streamable_http", true
	default:
		return "", false
	}
}

type mcpCreateReq struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Transport   string            `json:"transport"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	Enabled     *bool             `json:"enabled"`
	TimeoutMs   int               `json:"timeout_ms"`
}

func (r mcpCreateReq) toRow(uid string) (*model.MCPServer, error) {
	trans, okT := validTransport(r.Transport)
	if !okT {
		return nil, errBadTransport
	}
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return nil, errNameRequired
	}
	row := &model.MCPServer{
		UserID:      uid,
		Name:        name,
		Description: r.Description,
		Transport:   trans,
		TimeoutMs:   r.TimeoutMs,
		Enabled:     true,
	}
	if r.Enabled != nil {
		row.Enabled = *r.Enabled
	}
	switch trans {
	case "stdio":
		if strings.TrimSpace(r.Command) == "" {
			return nil, errCommandRequired
		}
		row.Command = r.Command
		if len(r.Args) > 0 {
			buf, _ := json.Marshal(r.Args)
			row.Args = string(buf)
		}
		if len(r.Env) > 0 {
			buf, _ := json.Marshal(r.Env)
			row.Env = string(buf)
		}
	case "streamable_http":
		if strings.TrimSpace(r.URL) == "" {
			return nil, errURLRequired
		}
		row.URL = r.URL
		if len(r.Headers) > 0 {
			buf, _ := json.Marshal(r.Headers)
			row.Headers = string(buf)
		}
	}
	if row.TimeoutMs <= 0 {
		row.TimeoutMs = 30000
	}
	return row, nil
}

func (h *MCPHandler) Create(c *gin.Context) {
	uid := currentUserID(c)
	var req mcpCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	row, err := req.toRow(uid)
	if err != nil {
		fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if err := h.db.Create(row).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	ok(c, gin.H{"id": row.ID})
}

type mcpListItem struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Transport   string            `json:"transport"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	Enabled     bool              `json:"enabled"`
	TimeoutMs   int               `json:"timeout_ms"`
	CreatedAt   string            `json:"created_at"`
}

func (h *MCPHandler) List(c *gin.Context) {
	uid := currentUserID(c)
	var rows []model.MCPServer
	if err := h.db.Where("user_id = ?", uid).Order("created_at desc").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	out := make([]mcpListItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, mcpListItem{
			ID:          r.ID,
			Name:        r.Name,
			Description: r.Description,
			Transport:   r.Transport,
			Command:     r.Command,
			Args:        mcp.DecodeArgs(r.Args),
			Env:         mcp.DecodeMap(r.Env),
			URL:         r.URL,
			Headers:     mcp.DecodeMap(r.Headers),
			Enabled:     r.Enabled,
			TimeoutMs:   r.TimeoutMs,
			CreatedAt:   r.CreatedAt.Format(time.RFC3339),
		})
	}
	ok(c, gin.H{"items": out})
}

type mcpUpdateReq struct {
	ID string `json:"id"`
	mcpCreateReq
}

func (h *MCPHandler) Update(c *gin.Context) {
	uid := currentUserID(c)
	var req mcpUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var row model.MCPServer
	if err := h.db.Where("id = ? AND user_id = ?", req.ID, uid).First(&row).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	patched, err := req.mcpCreateReq.toRow(uid)
	if err != nil {
		fail(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	updates := map[string]interface{}{
		"name":        patched.Name,
		"description": patched.Description,
		"transport":   patched.Transport,
		"command":     patched.Command,
		"args":        patched.Args,
		"env":         patched.Env,
		"url":         patched.URL,
		"headers":     patched.Headers,
		"timeout_ms":  patched.TimeoutMs,
	}
	// Only mutate `enabled` when the client explicitly sent it.
	//
	// `mcpCreateReq.Enabled` is `*bool` precisely so the absence of the
	// field is distinguishable from `false`. The previous version always
	// piped `patched.Enabled` into the updates map — and toRow's default
	// for the missing pointer is `true`. A "just rename this server" PATCH
	// from the frontend therefore silently flipped a previously-disabled
	// MCP back on, which then participated in the next agent loop without
	// the user's consent.
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if err := h.db.Model(&row).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	h.mgr.Invalidate(row.ID)
	ok(c, gin.H{"id": row.ID})
}

func (h *MCPHandler) Delete(c *gin.Context) {
	uid := currentUserID(c)
	var req idOnlyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if err := h.db.Where("id = ? AND user_id = ?", req.ID, uid).
		Delete(&model.MCPServer{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	h.mgr.Invalidate(req.ID)
	// Also strip the deleted id from any persona that referenced it so we
	// don't leave dangling pointers in EnabledMCPIDs. The LIKE pattern uses
	// the surrounding quote characters so we don't match arbitrary UUID
	// substrings (EnabledMCPIDs is stored as a JSON array of quoted strings,
	// e.g. `["abc","def"]`, so the id always appears as `"abc"`).
	var personas []model.Persona
	_ = h.db.Where("user_id = ? AND enabled_mcp_ids LIKE ?", uid, `%"`+req.ID+`"%`).
		Find(&personas).Error
	for i := range personas {
		ids := mcp.DecodeEnabledIDs(personas[i].EnabledMCPIDs)
		kept := make([]string, 0, len(ids))
		for _, id := range ids {
			if id != req.ID {
				kept = append(kept, id)
			}
		}
		if len(kept) != len(ids) {
			_ = h.db.Model(&personas[i]).
				Update("enabled_mcp_ids", mcp.EncodeEnabledIDs(kept)).Error
		}
	}
	ok(c, gin.H{"id": req.ID})
}

type mcpTestResult struct {
	OK    bool      `json:"ok"`
	Error string    `json:"error"`
	Tools []toolDTO `json:"tools"`
}

type toolDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Tools returns the cached tool descriptors for a single MCP server (or
// opens a fresh connection if none is cached). Unlike Test it does NOT force
// a reconnect — it's meant for inline previews in the persona detail page,
// so we want it cheap. Returns ok=false + error string on failure, mirroring
// the Test shape so the frontend can reuse rendering.
func (h *MCPHandler) Tools(c *gin.Context) {
	uid := currentUserID(c)
	var req idOnlyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var row model.MCPServer
	if err := h.db.Where("id = ? AND user_id = ?", req.ID, uid).First(&row).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	tools, err := h.mgr.ListTools(ctx, &row)
	if err != nil {
		ok(c, mcpTestResult{OK: false, Error: err.Error()})
		return
	}
	out := make([]toolDTO, 0, len(tools))
	for _, t := range tools {
		out = append(out, toolDTO{Name: t.Name, Description: t.Description})
	}
	ok(c, mcpTestResult{OK: true, Tools: out})
}

func (h *MCPHandler) Test(c *gin.Context) {
	uid := currentUserID(c)
	var req idOnlyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	var row model.MCPServer
	if err := h.db.Where("id = ? AND user_id = ?", req.ID, uid).First(&row).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	tools, err := h.mgr.Probe(ctx, &row)
	if err != nil {
		ok(c, mcpTestResult{OK: false, Error: err.Error()})
		return
	}
	out := make([]toolDTO, 0, len(tools))
	for _, t := range tools {
		out = append(out, toolDTO{Name: t.Name, Description: t.Description})
	}
	ok(c, mcpTestResult{OK: true, Tools: out})
}

type mcpImportReq struct {
	// JSON is the raw mcpServers config object as used by Claude Desktop /
	// Cursor. Either {"mcpServers": {...}} or just {...} is accepted.
	JSON string `json:"json"`
	// Replace controls whether servers with the same name are overwritten
	// (true) or skipped (false, default).
	Replace bool `json:"replace"`
}

type mcpImportStdioEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	// Optional fields that some configs include:
	URL     string            `json:"url"`
	Type    string            `json:"type"` // "stdio" / "streamable_http" / "http" / "sse"
	Headers map[string]string `json:"headers"`
}

type mcpImportResp struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors"`
}

// Import accepts a standard Claude/Cursor `mcpServers` JSON object and
// upserts each entry as an MCPServer row for the current user.
func (h *MCPHandler) Import(c *gin.Context) {
	uid := currentUserID(c)
	var req mcpImportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	raw := strings.TrimSpace(req.JSON)
	if raw == "" {
		fail(c, http.StatusBadRequest, 400, "json field is empty")
		return
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		fail(c, http.StatusBadRequest, 400, "not a JSON object: "+err.Error())
		return
	}
	servers := top
	if sub, hasSub := top["mcpServers"]; hasSub {
		servers = nil
		if err := json.Unmarshal(sub, &servers); err != nil {
			fail(c, http.StatusBadRequest, 400, "mcpServers must be an object: "+err.Error())
			return
		}
	}
	resp := mcpImportResp{}
	for name, raw := range servers {
		var entry mcpImportStdioEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			resp.Errors = append(resp.Errors, name+": "+err.Error())
			continue
		}
		row := &model.MCPServer{
			UserID:      uid,
			Name:        name,
			Description: "imported from mcpServers config",
			Transport:   "stdio",
			Enabled:     true,
			TimeoutMs:   30000,
		}
		// Heuristic for transport:
		// - explicit type wins
		// - else if url present → streamable_http
		// - else stdio
		switch strings.ToLower(entry.Type) {
		case "streamable_http", "http":
			row.Transport = "streamable_http"
		case "sse":
			// Legacy SSE config: still treat as streamable_http (modern spec).
			row.Transport = "streamable_http"
		case "", "stdio":
			if entry.URL != "" && entry.Command == "" {
				row.Transport = "streamable_http"
			}
		}
		switch row.Transport {
		case "stdio":
			if entry.Command == "" {
				resp.Errors = append(resp.Errors, name+": missing command")
				continue
			}
			row.Command = entry.Command
			if len(entry.Args) > 0 {
				buf, _ := json.Marshal(entry.Args)
				row.Args = string(buf)
			}
			if len(entry.Env) > 0 {
				buf, _ := json.Marshal(entry.Env)
				row.Env = string(buf)
			}
		case "streamable_http":
			if entry.URL == "" {
				resp.Errors = append(resp.Errors, name+": missing url")
				continue
			}
			row.URL = entry.URL
			if len(entry.Headers) > 0 {
				buf, _ := json.Marshal(entry.Headers)
				row.Headers = string(buf)
			}
		}

		var existing model.MCPServer
		err := h.db.Where("user_id = ? AND name = ?", uid, name).First(&existing).Error
		if err == nil {
			if !req.Replace {
				resp.Skipped++
				continue
			}
			row.ID = existing.ID
			if err := h.db.Model(&existing).Updates(map[string]interface{}{
				"description": row.Description,
				"transport":   row.Transport,
				"command":     row.Command,
				"args":        row.Args,
				"env":         row.Env,
				"url":         row.URL,
				"headers":     row.Headers,
				"enabled":     row.Enabled,
				"timeout_ms":  row.TimeoutMs,
			}).Error; err != nil {
				resp.Errors = append(resp.Errors, name+": "+err.Error())
				continue
			}
			h.mgr.Invalidate(existing.ID)
			resp.Imported++
			continue
		}
		if err := h.db.Create(row).Error; err != nil {
			resp.Errors = append(resp.Errors, name+": "+err.Error())
			continue
		}
		resp.Imported++
	}
	ok(c, resp)
}

// errors with stable text the frontend can show verbatim.
var (
	errBadTransport    = stringError("transport must be 'stdio' or 'streamable_http'")
	errNameRequired    = stringError("name is required")
	errCommandRequired = stringError("stdio transport requires command")
	errURLRequired     = stringError("streamable_http transport requires url")
)

type stringError string

func (s stringError) Error() string { return string(s) }
