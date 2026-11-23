package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// OrionModelHandler exposes the Orion catalogue sync to the model page: a
// status snapshot every viewer may read, a manual refresh for admins and the
// platform-wide credential management for system admins.
type OrionModelHandler struct {
	sync        *service.OrionModelSync
	credentials *service.OrionCredentialStore
}

// NewOrionModelHandler builds the handler.
func NewOrionModelHandler(
	syncService *service.OrionModelSync,
	credentials *service.OrionCredentialStore,
) *OrionModelHandler {
	return &OrionModelHandler{sync: syncService, credentials: credentials}
}

// orionModelStatusResponse is the read model behind the status bar.
type orionModelStatusResponse struct {
	Enabled             bool       `json:"enabled"`
	KeyConfigured       bool       `json:"key_configured"`
	LastSyncAt          *time.Time `json:"last_sync_at,omitempty"`
	ModelCount          int        `json:"model_count"`
	LastError           string     `json:"last_error,omitempty"`
	SyncIntervalSeconds int        `json:"sync_interval_seconds"`
}

// orionModelSyncResponse reports one manual refresh.
type orionModelSyncResponse struct {
	Total      int        `json:"total"`
	Created    int        `json:"created"`
	Updated    int        `json:"updated"`
	Deleted    int        `json:"deleted"`
	Skipped    int        `json:"skipped"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Status godoc
// @Summary      Orion 模型同步状态
// @Description  返回当前工作空间的 Orion 同步开关、凭证配置情况、上次同步时间与模型数量
// @Tags         模型管理
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "同步状态"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /models/orion/status [get]
func (h *OrionModelHandler) Status(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	if tenantID == 0 {
		c.Error(apperrors.NewBadRequestError("Workspace ID cannot be empty"))
		return
	}
	if h.sync == nil {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data":    orionModelStatusResponse{Enabled: false, SyncIntervalSeconds: 300},
		})
		return
	}

	status := h.sync.Status(ctx, tenantID)
	resp := orionModelStatusResponse{
		Enabled:             status.Enabled,
		KeyConfigured:       status.KeyConfigured,
		ModelCount:          status.ModelCount,
		LastError:           status.LastError,
		SyncIntervalSeconds: status.SyncIntervalSeconds,
	}
	if !status.LastSyncAt.IsZero() {
		lastSync := status.LastSyncAt
		resp.LastSyncAt = &lastSync
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": resp})
}

// Sync godoc
// @Summary      立即从 Orion 同步模型
// @Description  使用当前工作空间用户的 Orion 凭证拉取模型目录并对账落库
// @Tags         模型管理
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "同步结果"
// @Failure      400  {object}  errors.AppError         "未启用或未配置凭证"
// @Failure      409  {object}  errors.AppError         "同步进行中"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /models/orion/sync [post]
func (h *OrionModelHandler) Sync(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	if tenantID == 0 {
		c.Error(apperrors.NewBadRequestError("Workspace ID cannot be empty"))
		return
	}
	if h.sync == nil {
		c.Error(apperrors.NewBadRequestError("orion model mode is disabled"))
		return
	}

	result, err := h.sync.SyncTenant(ctx, tenantID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrOrionSyncInProgress):
			c.Error(apperrors.NewConflictError("orion model sync is already running"))
		case errors.Is(err, service.ErrOrionKeyMissing):
			c.Error(apperrors.NewBadRequestError(
				"orion api key is not configured; ask the platform administrator to configure it first",
			))
		case errors.Is(err, service.ErrOrionModelDisabled):
			c.Error(apperrors.NewBadRequestError("orion model mode is disabled"))
		case errors.Is(err, service.ErrOrionCatalogEmpty):
			logger.Warnf(ctx, "[orion-model] workspace %d sync returned no usable models", tenantID)
			c.Error(apperrors.NewInternalServerError(
				"Orion returned no usable models; existing models were left untouched",
			))
		default:
			logger.Warnf(ctx, "[orion-model] workspace %d sync failed: %v", tenantID, err)
			c.Error(apperrors.NewInternalServerError(
				"failed to sync models from Orion; existing models were left untouched",
			))
		}
		return
	}

	resp := orionModelSyncResponse{
		Total:     result.Total,
		Created:   result.Created,
		Updated:   result.Updated,
		Deleted:   result.Deleted,
		Skipped:   result.Skipped,
		StartedAt: result.StartedAt,
	}
	if !result.FinishedAt.IsZero() {
		finished := result.FinishedAt
		resp.FinishedAt = &finished
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": resp})
}

// orionAPIKeyResponse is the read model of the credential endpoints. The
// plaintext key never leaves the server; callers get a recognisable mask.
type orionAPIKeyResponse struct {
	Configured bool   `json:"configured"`
	KeyHint    string `json:"key_hint,omitempty"`
}

// saveOrionAPIKeyRequest is the PUT payload.
type saveOrionAPIKeyRequest struct {
	APIKey string `json:"api_key" binding:"required"`
}

// GetAPIKey godoc
// @Summary      查看全局 Orion API Key 配置状态
// @Description  返回全局 Orion License SK 是否已配置及其掩码（仅系统管理员）。原文永不返回。
// @Tags         模型管理
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{configured, key_hint}"
// @Failure      500  {object}  errors.AppError         "读取失败"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /models/orion/api-key [get]
func (h *OrionModelHandler) GetAPIKey(c *gin.Context) {
	ctx := c.Request.Context()
	if h.credentials == nil {
		c.Error(apperrors.NewInternalServerError("orion credential store is unavailable"))
		return
	}
	configured, err := h.credentials.Configured(ctx)
	if err != nil {
		logger.Errorf(ctx, "[orion-model] reading the global credential state failed: %v", err)
		c.Error(apperrors.NewInternalServerError("failed to read the orion credential state"))
		return
	}
	hint, err := h.credentials.KeyHint(ctx)
	if err != nil {
		logger.Errorf(ctx, "[orion-model] masking the global credential failed: %v", err)
		c.Error(apperrors.NewInternalServerError("failed to read the orion credential state"))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    orionAPIKeyResponse{Configured: configured, KeyHint: hint},
	})
}

// SaveAPIKey godoc
// @Summary      保存全局 Orion API Key
// @Description  校验并加密保存全局 Orion License SK（仅系统管理员）。保存为同步操作，
//               成功返回后立即全局生效；所有工作空间的模型同步在后台异步触发，
//               同步失败不影响保存结果。
// @Tags         模型管理
// @Accept       json
// @Produce      json
// @Param        body  body      saveOrionAPIKeyRequest  true  "Orion License SK"
// @Success      200   {object}  map[string]interface{}  "保存成功"
// @Failure      400   {object}  errors.AppError         "key 非法"
// @Failure      500   {object}  errors.AppError         "保存失败"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /models/orion/api-key [put]
func (h *OrionModelHandler) SaveAPIKey(c *gin.Context) {
	ctx := c.Request.Context()
	if h.credentials == nil {
		c.Error(apperrors.NewInternalServerError("orion credential store is unavailable"))
		return
	}
	var req saveOrionAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewBadRequestError("api_key is required"))
		return
	}
	if err := h.credentials.SaveAPIKey(ctx, req.APIKey); err != nil {
		if errors.Is(err, service.ErrOrionAPIKeyInvalid) {
			c.Error(apperrors.NewBadRequestError(err.Error()))
			return
		}
		logger.Errorf(ctx, "[orion-model] saving the global credential failed: %v", err)
		c.Error(apperrors.NewInternalServerError("failed to save the orion api key"))
		return
	}

	// The save itself is done; refresh every workspace in the background so
	// the new licence takes effect without waiting for the next sweep. A
	// failed refresh only shows up in the status, never as an HTTP error.
	if h.sync != nil {
		h.sync.SyncAllBackground()
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// orionAPIKeyRevokeResponse reports how many model rows the revoke removed.
type orionAPIKeyRevokeResponse struct {
	DeletedModels int `json:"deleted_models"`
}

// DeleteAPIKey godoc
// @Summary      取消全局 Orion API Key 授权
// @Description  清除全局 Orion License SK 并删除所有工作空间中由该凭证同步出来的模型（仅系统管理员）；先清凭证再清模型，避免被重新写回。
// @Tags         模型管理
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{deleted_models}"
// @Failure      500  {object}  errors.AppError         "取消失败"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /models/orion/api-key [delete]
func (h *OrionModelHandler) DeleteAPIKey(c *gin.Context) {
	ctx := c.Request.Context()
	if h.credentials == nil {
		c.Error(apperrors.NewInternalServerError("orion credential store is unavailable"))
		return
	}

	// Clear the credential first. Every sync path treats an empty key as
	// "not configured" and bails out, so nothing can re-create the rows while
	// they are being removed.
	if _, err := h.credentials.ClearAPIKey(ctx); err != nil {
		logger.Errorf(ctx, "[orion-model] clearing the global credential failed: %v", err)
		c.Error(apperrors.NewInternalServerError("failed to clear the orion api key"))
		return
	}

	// Then drop the models in the same request: the UI refreshes its list right
	// after the response, so a background purge would leave stale cards behind.
	deleted := 0
	if h.sync != nil {
		count, err := h.sync.PurgeAll(ctx)
		if err != nil {
			logger.Warnf(ctx, "[orion-model] removing the models after revoke failed: %v", err)
			c.Error(apperrors.NewInternalServerError(
				"the orion api key was cleared but removing the synced models failed"))
			return
		}
		deleted = count
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    orionAPIKeyRevokeResponse{DeletedModels: deleted},
	})
}
