package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
)

const (
	// orionTokenHeader is the header Orion itself uses for its session JWT.
	orionTokenHeader = "NS-TOKEN"
	// orionTokenHeaderAlt lets a reverse proxy or service forward the
	// assertion without colliding with the NS-TOKEN name.
	orionTokenHeaderAlt = "X-Orion-Token"
)

// OrionExchange godoc
// @Summary      用 Orion 凭据换取 WeKnora 会话
// @Description  校验调用方携带的 Orion 凭据（NS-TOKEN），首次进入时按 Orion 的用户编号与机构编号开通本地用户、工作空间与成员关系，随后签发 WeKnora 的访问令牌与续期令牌。
// @Description  用户编号与机构编号只能取自 Orion 的校验结果，请求体中传入的编号一律忽略。
// @Tags         认证
// @Accept       json
// @Produce      json
// @Success      200  {object} types.LoginResponse  "会话"
// @Failure      400  {object} errors.AppError      "未携带 Orion 凭据"
// @Failure      401  {object} errors.AppError      "Orion 凭据无效"
// @Failure      403  {object} errors.AppError      "Orion 账号不可用"
// @Failure      503  {object} errors.AppError      "Orion 身份服务不可用"
// @Router       /auth/orion/exchange [post]
//
// OrionExchange is the only unauthenticated endpoint that can mint a session.
// Browsers cannot attach custom headers to a plain link navigation, so the
// assertion is accepted from four carriers in this order: NS-TOKEN header,
// X-Orion-Token header, request body, then the NS-TOKEN cookie (which the
// browser sends automatically for same-host deployments).
func (h *AuthHandler) OrionExchange(c *gin.Context) {
	ctx := c.Request.Context()

	assertion := orionAssertionFromRequest(c)
	if assertion == "" {
		// Never echo the assertion or its absence details beyond this.
		logger.Warn(ctx, "Orion SSO exchange rejected: no assertion supplied")
		c.Error(apperrors.NewBadRequestError("Orion credential is required"))
		return
	}

	response, err := h.userService.LoginWithOrion(ctx, assertion)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrOrionDisabled):
			c.Error(apperrors.NewBadRequestError("Orion single sign-on is not enabled"))
		case errors.Is(err, service.ErrOrionCredentialInvalid):
			c.Error(apperrors.NewUnauthorizedError("Orion credential is invalid or expired"))
		case errors.Is(err, service.ErrOrionAccountInactive):
			c.Error(apperrors.NewForbiddenError("Orion account is not active"))
		case errors.Is(err, service.ErrOrionUnavailable):
			c.Error(apperrors.NewServiceUnavailableError("Orion identity service is unavailable"))
		default:
			logger.Errorf(ctx, "Orion SSO exchange failed: %v", err)
			c.Error(apperrors.NewInternalServerError("Failed to exchange Orion credential"))
		}
		return
	}

	logger.Infof(ctx, "Orion SSO exchange succeeded for user %s", response.User.ID)
	c.JSON(http.StatusOK, response)
}

// orionAssertionFromRequest extracts the Orion assertion from every carrier a
// deployment realistically uses.
func orionAssertionFromRequest(c *gin.Context) string {
	if value := strings.TrimSpace(c.GetHeader(orionTokenHeader)); value != "" {
		return value
	}
	if value := strings.TrimSpace(c.GetHeader(orionTokenHeaderAlt)); value != "" {
		return value
	}
	// Orion's own extractor tries header, then query, then cookie, and links
	// produced by its console carry the assertion as ?NS-TOKEN=. Accepting the
	// query form keeps those links working without a proxy in between.
	if value := strings.TrimSpace(c.Query(orionTokenHeader)); value != "" {
		return value
	}
	if value := strings.TrimSpace(c.Query("ns_token")); value != "" {
		return value
	}
	if value := orionAssertionFromBody(c); value != "" {
		return value
	}
	if value, err := c.Cookie(orionTokenHeader); err == nil {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// orionAssertionFromBody reads {"token": "..."} (JSON) or token=... (form).
// It deliberately switches on Content-Type so a JSON body is never consumed
// by form parsing.
func orionAssertionFromBody(c *gin.Context) string {
	contentType := c.ContentType()
	switch {
	case strings.Contains(contentType, "application/json"):
		var payload struct {
			Token   string `json:"token"`
			NSToken string `json:"ns_token"`
		}
		if err := c.ShouldBindJSON(&payload); err != nil {
			return ""
		}
		if value := strings.TrimSpace(payload.Token); value != "" {
			return value
		}
		return strings.TrimSpace(payload.NSToken)
	case strings.Contains(contentType, "application/x-www-form-urlencoded"),
		strings.Contains(contentType, "multipart/form-data"):
		return strings.TrimSpace(c.PostForm("token"))
	default:
		return ""
	}
}
