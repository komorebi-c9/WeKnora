package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/infrastructure/orion"
	"github.com/Tencent/WeKnora/internal/logger"
)

const (
	// orionTokenHeader is the header Orion itself uses for its session JWT.
	orionTokenHeader = "NS-TOKEN"
	// orionTokenHeaderAlt lets a reverse proxy or service forward the
	// assertion without colliding with the NS-TOKEN name.
	orionTokenHeaderAlt = "X-Orion-Token"
	// orionAPIKeyHeader is the header Orion's gateway uses for a License SK.
	// It is the machine-facing counterpart of NS-TOKEN.
	orionAPIKeyHeader = "NS-APIKEY"
	// orionAPIKeyHeaderSK is the name Orion is migrating the SK header to.
	// Both names are accepted everywhere a License SK may arrive, so a jump
	// link or proxy that switches to NS-SK keeps working without a release on
	// this side (the outbound side sends both as well).
	orionAPIKeyHeaderSK = "NS-SK"
	// orionAPIKeyHeaderAlt is the forwarding-friendly alias for the SK, with
	// the same role as orionTokenHeaderAlt.
	orionAPIKeyHeaderAlt = "X-Orion-Api-Key"
	// orionAPIKeyQuery is the short query carrier a hand-built jump link uses
	// for an SK ("?sk=sk-...").
	orionAPIKeyQuery = "sk"
)

// OrionExchange godoc
// @Summary      用 Orion 凭据换取 WeKnora 会话
// @Description  校验调用方携带的 Orion 凭据（NS-TOKEN 会话凭据或 License SK），首次进入时按 Orion 的用户编号与机构编号开通本地用户、工作空间与成员关系，随后签发 WeKnora 的访问令牌与续期令牌。
// @Description  同时支持两种凭据：请求带 NS-TOKEN 时优先使用它；完全没有 NS-TOKEN 时才解析 License SK（sk-...）。两者都没有则拒绝。
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
// Browsers cannot attach custom headers to a plain link navigation, so a
// credential is accepted from several carriers; see
// orionCredentialFromRequest for the order.
func (h *AuthHandler) OrionExchange(c *gin.Context) {
	ctx := c.Request.Context()

	credential := orionCredentialFromRequest(c)
	if credential == "" {
		// Never echo the credential or its absence details beyond this.
		logger.Warn(ctx, "Orion SSO exchange rejected: no credential supplied")
		c.Error(apperrors.NewBadRequestError("Orion credential is required"))
		return
	}

	response, err := h.userService.LoginWithOrion(ctx, credential)
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

// orionCredentialFromRequest extracts the Orion credential from every carrier
// a deployment realistically uses.
//
// Two credential shapes are accepted, mirroring Orion's own authentication:
// a session assertion (NS-TOKEN) and a License SK (sk-...). An assertion
// always wins: the SK carriers are only consulted once every assertion carrier
// is empty, so existing deployments keep their behaviour until they start
// sending SKs. Explicit carriers (header / query / body) outrank cookies, so a
// stale session cookie cannot mask a fresh credential carried by the link.
//
// The License SK is accepted under both header names Orion has used for it,
// NS-APIKEY and NS-SK, on every carrier (header / query / body / cookie): a
// jump link or gateway that adopts the new name must not need a change here.
func orionCredentialFromRequest(c *gin.Context) string {
	// 1. Session assertion, explicit carriers.
	if value := strings.TrimSpace(c.GetHeader(orionTokenHeader)); value != "" {
		return value
	}
	if value := strings.TrimSpace(c.GetHeader(orionTokenHeaderAlt)); value != "" {
		return value
	}
	// 2. License SK, explicit header carriers. NS-APIKEY is the historical
	// name, NS-SK the one Orion is migrating to; both are read.
	if value := strings.TrimSpace(c.GetHeader(orionAPIKeyHeader)); value != "" {
		return value
	}
	if value := strings.TrimSpace(c.GetHeader(orionAPIKeyHeaderSK)); value != "" {
		return value
	}
	if value := strings.TrimSpace(c.GetHeader(orionAPIKeyHeaderAlt)); value != "" {
		return value
	}
	// Orion's gateway also accepts the SK as Authorization: Bearer (legacy
	// SDKs send it that way), so honour it when the token looks like an SK.
	// A plain bearer value is left alone: it belongs to the upstream call the
	// client is making, not to Orion.
	if value := orionBearerAPIKey(c); value != "" {
		return value
	}
	// 3. Session assertion via query. Orion's console links carry it as
	// ?NS-TOKEN=, which keeps them working without a proxy in between.
	if value := strings.TrimSpace(c.Query(orionTokenHeader)); value != "" {
		return value
	}
	if value := strings.TrimSpace(c.Query("ns_token")); value != "" {
		return value
	}
	// 4. License SK via query, the shape an "open from Orion" link uses when
	// it ships an SK instead of a session cookie.
	if value := strings.TrimSpace(c.Query(orionAPIKeyHeader)); value != "" {
		return value
	}
	if value := strings.TrimSpace(c.Query("ns_apikey")); value != "" {
		return value
	}
	if value := strings.TrimSpace(c.Query(orionAPIKeyHeaderSK)); value != "" {
		return value
	}
	if value := strings.TrimSpace(c.Query("ns_sk")); value != "" {
		return value
	}
	if value := strings.TrimSpace(c.Query(orionAPIKeyQuery)); value != "" {
		return value
	}
	// 5. Request body (JSON or form).
	if value := orionCredentialFromBody(c); value != "" {
		return value
	}
	// 6. Cookies, which the browser sends automatically for same-host
	// deployments. Assertion first, then SK.
	if value, err := c.Cookie(orionTokenHeader); err == nil {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	if value, err := c.Cookie(orionAPIKeyHeader); err == nil {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	if value, err := c.Cookie(orionAPIKeyHeaderSK); err == nil {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// orionBearerAPIKey returns the SK carried as "Authorization: Bearer sk-...".
// Only an sk-shaped value is returned: a generic bearer token is somebody
// else's credential and must not be forwarded to Orion.
func orionBearerAPIKey(c *gin.Context) string {
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	if header == "" {
		return ""
	}
	scheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	value = strings.TrimSpace(value)
	if !orion.APIKeyShape(value) {
		return ""
	}
	return value
}

// orionCredentialFromBody reads {"token": "..."} (JSON) or token=... (form)
// and its SK counterparts. It deliberately switches on Content-Type so a JSON
// body is never consumed by form parsing. The assertion wins inside the body
// for the same reason it wins everywhere else.
func orionCredentialFromBody(c *gin.Context) string {
	contentType := c.ContentType()
	switch {
	case strings.Contains(contentType, "application/json"):
		var payload struct {
			Token    string `json:"token"`
			NSToken  string `json:"ns_token"`
			APIKey   string `json:"api_key"`
			NSAPIKey string `json:"ns_apikey"`
			NSSK     string `json:"ns_sk"`
			SKHeader string `json:"NS-SK"`
			SK       string `json:"sk"`
		}
		if err := c.ShouldBindJSON(&payload); err != nil {
			return ""
		}
		for _, candidate := range []string{
			payload.Token,
			payload.NSToken,
			payload.APIKey,
			payload.NSAPIKey,
			payload.SKHeader,
			payload.NSSK,
			payload.SK,
		} {
			if value := strings.TrimSpace(candidate); value != "" {
				return value
			}
		}
		return ""
	case strings.Contains(contentType, "application/x-www-form-urlencoded"),
		strings.Contains(contentType, "multipart/form-data"):
		for _, key := range []string{
			"token",
			"ns_token",
			orionAPIKeyHeader,
			"ns_apikey",
			orionAPIKeyHeaderSK,
			"ns_sk",
			orionAPIKeyQuery,
		} {
			if value := strings.TrimSpace(c.PostForm(key)); value != "" {
				return value
			}
		}
		return ""
	default:
		return ""
	}
}
