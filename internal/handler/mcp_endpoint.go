package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/infrastructure/orion"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
)

// MCPEndpointHandler manages the workspace's published MCP server endpoints.
type MCPEndpointHandler struct {
	svc interfaces.MCPEndpointService
	// The three dependencies below back the optional Orion push after a
	// successful create. They are nil in partially-wired tests, in which case
	// the push is skipped.
	cfg         *config.Config
	userService interfaces.UserService
	credentials *service.OrionCredentialStore
}

// NewMCPEndpointHandler creates the MCP endpoint management handler.
func NewMCPEndpointHandler(
	svc interfaces.MCPEndpointService,
	cfg *config.Config,
	userService interfaces.UserService,
	credentials *service.OrionCredentialStore,
) *MCPEndpointHandler {
	return &MCPEndpointHandler{
		svc:         svc,
		cfg:         cfg,
		userService: userService,
		credentials: credentials,
	}
}

type mcpEndpointRequest struct {
	Name               *string   `json:"name"`
	Description        *string   `json:"description"`
	Enabled            *bool     `json:"enabled"`
	KnowledgeBaseIDs   *[]string `json:"knowledge_base_ids"`
	Tools              *[]string `json:"tools"`
	DefaultAgentID     *string   `json:"default_agent_id"`
	RateLimitPerMinute *int      `json:"rate_limit_per_minute"`
	// PublicBaseURL is the externally visible base the caller reached this
	// deployment on (e.g. "http://host:9298/lolr-kb"), used only to compose
	// the MCP URL pushed to Orion. Optional: without it the push is skipped.
	PublicBaseURL *string `json:"public_base_url"`
}

func stringValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ListToolCatalog returns the tools an endpoint can expose, grouped.
//
// @Summary      获取 MCP 端点工具目录
// @Description  返回工作空间 MCP 端点可勾选的工具清单、分组和默认勾选项
// @Tags         MCP端点
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "工具目录"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /mcp-endpoints/tools [get]
func (h *MCPEndpointHandler) ListToolCatalog(c *gin.Context) {
	groups := types.MCPEndpointToolGroups()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"groups":        groups,
			"tools":         types.MCPEndpointToolCatalog(),
			"default_tools": types.DefaultMCPEndpointTools(),
		},
	})
}

// CreateMCPEndpoint creates an endpoint and returns it with the one-time token.
//
// @Summary      创建 MCP 端点
// @Description  为当前工作空间发布一个 MCP 端点，响应中的 token 只返回一次
// @Tags         MCP端点
// @Accept       json
// @Produce      json
// @Param        request  body      object  true  "端点配置：name、description、enabled、knowledge_base_ids、tools 等"
// @Success      201      {object}  map[string]interface{}  "创建的端点，含一次性 token"
// @Failure      400      {object}  map[string]interface{}  "请求参数错误"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /mcp-endpoints [post]
func (h *MCPEndpointHandler) CreateMCPEndpoint(c *gin.Context) {
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	var req mcpEndpointRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	tools := types.DefaultMCPEndpointTools()
	if req.Tools != nil {
		tools = *req.Tools
	}
	var kbIDs []string
	if req.KnowledgeBaseIDs != nil {
		kbIDs = *req.KnowledgeBaseIDs
	}
	rate := 0
	if req.RateLimitPerMinute != nil {
		rate = *req.RateLimitPerMinute
	}
	ep, token, err := h.svc.Create(c.Request.Context(), tenantID, &types.MCPEndpoint{
		Name:               stringValue(req.Name),
		Description:        stringValue(req.Description),
		Enabled:            enabled,
		KnowledgeBaseIDs:   types.StringArray(kbIDs),
		Tools:              types.StringArray(tools),
		DefaultAgentID:     stringValue(req.DefaultAgentID),
		RateLimitPerMinute: rate,
	})
	if err != nil {
		writeMCPEndpointMgmtError(c, err)
		return
	}
	// Best-effort background registration in Orion's MCP list. It must not
	// delay or fail the create response.
	h.pushMCPToOrion(tenantID, ep, token, stringValue(req.PublicBaseURL))
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": mcpEndpointResponse(ep, token)})
}

// ListMCPEndpoints lists the workspace endpoints without tokens.
//
// @Summary      获取 MCP 端点列表
// @Tags         MCP端点
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "端点列表"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /mcp-endpoints [get]
func (h *MCPEndpointHandler) ListMCPEndpoints(c *gin.Context) {
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	rows, err := h.svc.List(c.Request.Context(), tenantID)
	if err != nil {
		writeMCPEndpointMgmtError(c, err)
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, ep := range rows {
		out = append(out, mcpEndpointResponse(ep, ""))
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": out})
}

// GetMCPEndpoint returns one endpoint.
//
// @Summary      获取 MCP 端点详情
// @Tags         MCP端点
// @Produce      json
// @Param        endpoint_id  path      string  true  "端点 ID"
// @Success      200          {object}  map[string]interface{}  "端点详情"
// @Failure      404          {object}  map[string]interface{}  "端点不存在"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /mcp-endpoints/{endpoint_id} [get]
func (h *MCPEndpointHandler) GetMCPEndpoint(c *gin.Context) {
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	id := secutils.SanitizeForLog(c.Param("endpoint_id"))
	ep, err := h.svc.Get(c.Request.Context(), tenantID, id)
	if err != nil {
		writeMCPEndpointMgmtError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": mcpEndpointResponse(ep, "")})
}

// UpdateMCPEndpoint applies a partial update; omitted fields are kept.
//
// @Summary      更新 MCP 端点
// @Tags         MCP端点
// @Accept       json
// @Produce      json
// @Param        endpoint_id  path      string  true  "端点 ID"
// @Param        request      body      object  true  "要更新的字段，未提供的字段保持不变"
// @Success      200          {object}  map[string]interface{}  "更新后的端点"
// @Failure      400          {object}  map[string]interface{}  "请求参数错误"
// @Failure      404          {object}  map[string]interface{}  "端点不存在"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /mcp-endpoints/{endpoint_id} [put]
func (h *MCPEndpointHandler) UpdateMCPEndpoint(c *gin.Context) {
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	id := secutils.SanitizeForLog(c.Param("endpoint_id"))
	var req mcpEndpointRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ep, err := h.svc.Update(c.Request.Context(), tenantID, id, interfaces.MCPEndpointUpdate{
		Name:               req.Name,
		Description:        req.Description,
		Enabled:            req.Enabled,
		KnowledgeBaseIDs:   req.KnowledgeBaseIDs,
		Tools:              req.Tools,
		DefaultAgentID:     req.DefaultAgentID,
		RateLimitPerMinute: req.RateLimitPerMinute,
	})
	if err != nil {
		writeMCPEndpointMgmtError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": mcpEndpointResponse(ep, "")})
}

// DeleteMCPEndpoint soft-deletes an endpoint; connected clients lose access.
//
// @Summary      删除 MCP 端点
// @Tags         MCP端点
// @Produce      json
// @Param        endpoint_id  path      string  true  "端点 ID"
// @Success      200          {object}  handler.SuccessResponse  "删除成功"
// @Failure      404          {object}  map[string]interface{}  "端点不存在"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /mcp-endpoints/{endpoint_id} [delete]
func (h *MCPEndpointHandler) DeleteMCPEndpoint(c *gin.Context) {
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	id := secutils.SanitizeForLog(c.Param("endpoint_id"))
	if err := h.svc.Delete(c.Request.Context(), tenantID, id); err != nil {
		writeMCPEndpointMgmtError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// RotateMCPEndpointToken issues a fresh token and returns it once.
//
// @Summary      轮换 MCP 端点令牌
// @Description  生成新令牌并立即作废旧令牌，响应中的 token 只返回一次
// @Tags         MCP端点
// @Produce      json
// @Param        endpoint_id  path      string  true  "端点 ID"
// @Success      200          {object}  map[string]interface{}  "含新 token 的端点"
// @Failure      404          {object}  map[string]interface{}  "端点不存在"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /mcp-endpoints/{endpoint_id}/rotate-token [post]
func (h *MCPEndpointHandler) RotateMCPEndpointToken(c *gin.Context) {
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	id := secutils.SanitizeForLog(c.Param("endpoint_id"))
	ep, token, err := h.svc.RotateToken(c.Request.Context(), tenantID, id)
	if err != nil {
		writeMCPEndpointMgmtError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": mcpEndpointResponse(ep, token)})
}

// MCPEndpointPath is the public path prefix the MCP server is mounted on.
const MCPEndpointPath = "/mcp/"

func mcpEndpointResponse(ep *types.MCPEndpoint, token string) gin.H {
	row := gin.H{
		"id":                    ep.ID,
		"tenant_id":             ep.TenantID,
		"name":                  ep.Name,
		"description":           ep.Description,
		"enabled":               ep.Enabled,
		"token_hint":            ep.TokenHint,
		"knowledge_base_ids":    []string(ep.KnowledgeBaseIDs),
		"tools":                 []string(ep.Tools),
		"default_agent_id":      ep.DefaultAgentID,
		"rate_limit_per_minute": ep.RateLimitPerMinute,
		"path":                  MCPEndpointPath + ep.ID,
		"last_used_at":          ep.LastUsedAt,
		"created_at":            ep.CreatedAt,
		"updated_at":            ep.UpdatedAt,
	}
	if strings.TrimSpace(token) != "" {
		row["token"] = token
	}
	return row
}

func writeMCPEndpointMgmtError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrMCPEndpointNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "mcp endpoint not found"})
	default:
		var appErr *apperrors.AppError
		if errors.As(err, &appErr) {
			switch appErr.Code {
			case apperrors.ErrNotFound:
				c.JSON(http.StatusNotFound, gin.H{"error": appErr.Message})
				return
			case apperrors.ErrBadRequest, apperrors.ErrValidation:
				c.JSON(http.StatusBadRequest, gin.H{"error": appErr.Message})
				return
			}
		}
		logger.Error(c.Request.Context(), "mcp endpoint management failed", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "operation failed"})
	}
}

const (
	orionMCPNamePrefix        = "lolr"
	orionMCPNameMaxLen        = 100
	orionMCPDescriptionMaxLen = 4096
)

// pushMCPToOrion registers a freshly created endpoint in Orion's MCP list.
//
// The push is detached from the request: it runs in its own goroutine with a
// background context, so neither a slow Orion nor a disconnected client can
// delay the create response. Failures are logged and never retried — Orion
// rejects a duplicate name instead of overwriting, so a retry cannot help.
func (h *MCPEndpointHandler) pushMCPToOrion(
	tenantID uint64, ep *types.MCPEndpoint, token, publicBaseURL string,
) {
	logCtx := context.Background()
	if h == nil || ep == nil {
		return
	}
	if h.cfg == nil || h.credentials == nil || h.userService == nil {
		logger.Warnf(logCtx, "[orion-mcp] push skipped: handler dependencies not wired, endpoint %s", ep.ID)
		return
	}
	serviceRoot := strings.TrimRight(strings.TrimSpace(h.cfg.OrionBaseURL()), "/")
	if serviceRoot == "" {
		logger.Infof(logCtx, "[orion-mcp] push skipped: orion base url not configured, endpoint %s", ep.ID)
		return
	}
	if !strings.HasSuffix(serviceRoot, service.OrionGatewayPath) {
		serviceRoot += service.OrionGatewayPath
	}
	logger.Infof(logCtx, "[orion-mcp] push start: tenant=%d endpoint=%s orion=%s mcp_base=%q",
		tenantID, ep.ID, serviceRoot, publicBaseURL)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), orion.DefaultAssetsTimeout)
		defer cancel()
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Warnf(ctx, "[orion-mcp] push panicked for endpoint %s: %v", ep.ID, recovered)
			}
		}()

		apiKey, err := h.credentials.GetAPIKey(ctx)
		if err != nil {
			logger.Warnf(ctx, "[orion-mcp] reading the orion api key failed, endpoint %s: %v", ep.ID, err)
			return
		}
		if apiKey == "" {
			logger.Infof(ctx, "[orion-mcp] orion api key not configured; skipping endpoint %s", ep.ID)
			return
		}

		user, err := h.userService.GetUserByTenantID(ctx, tenantID)
		if err != nil || user == nil {
			logger.Warnf(ctx, "[orion-mcp] no user for tenant %d; skipping endpoint %s: %v", tenantID, ep.ID, err)
			return
		}
		userID := strings.TrimSpace(user.ID)
		if userID == "" {
			logger.Warnf(ctx, "[orion-mcp] tenant %d has no user id; skipping endpoint %s", tenantID, ep.ID)
			return
		}
		// Orion user ids are numeric; a non-numeric id means the workspace owner
		// signed in through local auth. The push is still attempted so the
		// upstream verdict lands in the log instead of being guessed here.
		if _, err := strconv.ParseInt(userID, 10, 64); err != nil {
			logger.Warnf(ctx, "[orion-mcp] tenant %d user %q is not a numeric orion id; pushing anyway",
				tenantID, userID)
		}

		// The MCP URL Orion connects to is the address the console showed the
		// operator, so it comes from the request rather than from Orion config.
		base := normalizeMCPPublicBaseURL(publicBaseURL)
		if base == "" {
			logger.Warnf(ctx, "[orion-mcp] no valid public base url for tenant %d; skipping endpoint %s",
				tenantID, ep.ID)
			return
		}

		// name and showName mirror the identifier the console shows in its
		// client snippets (frontend mcpServerKey), so the same endpoint has
		// one name everywhere. Orion requires both to be unique in the org.
		name := orionMCPName(ep.Name, ep.ID)
		mcpURL := base + MCPEndpointPath + ep.ID
		input := orion.CreateMCPInput{
			Name:              name,
			ShowName:          name,
			Description:       truncateMCPDescription(ep.Description),
			TransportProtocol: 2, // streamable-http
			InjectionMode:     2, // aggregated: one record per endpoint
			TransportConfig: map[string]any{
				"url": mcpURL,
				"headers": map[string]any{
					"Authorization": "Bearer " + token,
				},
			},
			CreatedBy: userID,
		}
		if err := orion.CreateMCP(ctx, serviceRoot, apiKey, input); err != nil {
			logger.Warnf(ctx, "[orion-mcp] push failed: tenant=%d endpoint=%s name=%s err=%v",
				tenantID, ep.ID, name, err)
			return
		}
		logger.Infof(ctx, "[orion-mcp] pushed endpoint: tenant=%d endpoint=%s name=%s url=%s",
			tenantID, ep.ID, name, mcpURL)
	}()
}

// normalizeMCPPublicBaseURL accepts only an absolute http(s) origin/path with
// no query or fragment; anything else disables the push instead of sending a
// half-formed URL to Orion.
func normalizeMCPPublicBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	return raw
}

// orionMCPName mirrors the frontend mcpServerKey helper: lowercase ASCII slug,
// non-alphanumeric runs collapsed to "-", the "lolr-" prefix, and the endpoint
// id (first 8 alphanumerics) when the name yields no slug at all.
func orionMCPName(name, id string) string {
	var slug strings.Builder
	dashed := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			slug.WriteRune(r)
			dashed = false
			continue
		}
		if slug.Len() > 0 && !dashed {
			slug.WriteByte('-')
			dashed = true
		}
	}
	key := strings.Trim(slug.String(), "-")
	if key == "" {
		var idSlug strings.Builder
		for _, r := range strings.ToLower(id) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				idSlug.WriteRune(r)
				if idSlug.Len() == 8 {
					break
				}
			}
		}
		key = idSlug.String()
	}
	if key == "" {
		return orionMCPNamePrefix
	}
	out := orionMCPNamePrefix + "-" + key
	if len(out) > orionMCPNameMaxLen {
		out = out[:orionMCPNameMaxLen]
	}
	return out
}

func truncateMCPDescription(description string) string {
	if utf8.RuneCountInString(description) <= orionMCPDescriptionMaxLen {
		return description
	}
	return string([]rune(description)[:orionMCPDescriptionMaxLen])
}
