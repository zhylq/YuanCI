package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/integration"
)

const importCookieName = "__Host-yuanci_import"

func importCookie(value string) *http.Cookie {
	return &http.Cookie{Name: importCookieName, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 300}
}
func (a *API) integrationSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := a.oauth.Integrations.Settings(r.Context(), browserFrom(r).token)
	if err != nil {
		integrationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}
func (a *API) saveIntegration(w http.ResponseWriter, r *http.Request) {
	var input integration.AppInput
	if err := decodeJSON(w, r, &input); err != nil {
		return
	}
	if err := a.oauth.Integrations.Save(r.Context(), browserFrom(r).token, input); err != nil {
		integrationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *API) authorizeIntegration(w http.ResponseWriter, r *http.Request) {
	state, nonce := identity.NewToken(), identity.NewToken()
	target, err := a.oauth.Integrations.Start(r.Context(), browserFrom(r).token, state, nonce)
	if err != nil {
		integrationError(w, err)
		return
	}
	http.SetCookie(w, importCookie(nonce))
	w.Header().Set("Referrer-Policy", "no-referrer")
	writeJSON(w, http.StatusOK, map[string]string{"authorization_url": target})
}
func (a *API) finishIntegration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	flows := r.CookiesNamed(importCookieName)
	sessions := r.CookiesNamed(identity.CookieName)
	if err != nil || len(query["state"]) != 1 || len(query["code"]) > 1 || len(query["error"]) > 1 || len(flows) != 1 || len(sessions) != 1 || len(r.Header.Values("Authorization")) != 0 {
		integrationError(w, identity.ErrOAuthFlow)
		return
	}
	code := query.Get("code")
	if len(query["error"]) != 0 {
		code = ""
	}
	err = a.oauth.Integrations.Finish(r.Context(), sessions[0].Value, query.Get("state"), flows[0].Value, code)
	cookie := importCookie("")
	cookie.MaxAge = -1
	http.SetCookie(w, cookie)
	if err != nil {
		integrationError(w, err)
		return
	}
	// Setup URL parameters such as installation_id are never consumed here.
	// This callback never creates or rotates a browser session.
	http.Redirect(w, r, "/settings/repositories", http.StatusSeeOther)
}
func (a *API) integrationInstallations(w http.ResponseWriter, r *http.Request) {
	items, err := a.oauth.Integrations.Installations(r.Context(), browserFrom(r).token)
	if err != nil {
		integrationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (a *API) integrationRepositories(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	page := 1
	if err != nil || len(q["page"]) > 1 {
		integrationError(w, integration.ErrConfig)
		return
	}
	if len(q["page"]) == 1 {
		page, err = strconv.Atoi(q.Get("page"))
		if err != nil || page < 1 || page > 100 {
			integrationError(w, integration.ErrConfig)
			return
		}
	}
	items, err := a.oauth.Integrations.Repositories(r.Context(), browserFrom(r).token, r.PathValue("installationID"), page)
	if err != nil {
		integrationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}
func (a *API) importRepositories(w http.ResponseWriter, r *http.Request) {
	var input struct {
		InstallationID string   `json:"installation_id"`
		RepositoryIDs  []string `json:"repository_ids"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		return
	}
	items, err := a.oauth.Integrations.Import(r.Context(), browserFrom(r).token, input.InstallationID, input.RepositoryIDs)
	if err != nil {
		integrationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func integrationError(w http.ResponseWriter, err error) {
	if accessError(w, err) {
		return
	}
	switch {
	case errors.Is(err, integration.ErrConfig):
		writeProblem(w, 422, "invalid integration", "请检查 App ID、RSA 私钥与已启用的登录应用是否匹配，以及分页和仓库选择参数。")
	case errors.Is(err, integration.ErrStale):
		writeProblem(w, 409, "authorization changed", "配置或授权已变化、过期。请刷新设置，必要时重新验证私钥并授权发现仓库；项目冲突不会自动迁移。")
	case errors.Is(err, integration.ErrAccess):
		writeProblem(w, 403, "integration access denied", "无法验证 GitHub 访问权限。请使用已绑定的管理员账号，并检查 App 安装、仓库管理员权限及授权状态。")
	case errors.Is(err, integration.ErrRate):
		w.Header().Set("Retry-After", "60")
		writeProblem(w, 429, "GitHub rate limited", "GitHub 请求受限，请稍后重新授权或重试。")
	case errors.Is(err, integration.ErrRemote):
		writeProblem(w, 502, "GitHub unavailable", "暂时无法验证 GitHub 响应，请稍后重试；未导入任何项目。")
	case errors.Is(err, identity.ErrOAuthFlow):
		writeProblem(w, 400, "invalid authorization", "授权流程无效或过期，请返回仓库接入设置，在同一浏览器重新授权。")
	default:
		writeProblem(w, 503, "integration unavailable", "暂时无法完成仓库接入，请检查服务状态后重试。")
	}
}
