package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/yuanci/yuanci/internal/gitee"
	"github.com/yuanci/yuanci/internal/identity"
	"github.com/yuanci/yuanci/internal/scm"
)

const giteeCookieName = "__Host-yuanci_gitee_import"

func giteeCookie(value string) *http.Cookie {
	cookie := importCookie(value)
	cookie.Name = giteeCookieName
	return cookie
}
func (a *API) giteeSettings(w http.ResponseWriter, r *http.Request) {
	snap, err := a.oauth.Gitee.Store.GiteeContext(r.Context(), browserFrom(r).token, false)
	if err != nil {
		giteeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"authorization": snap.Grant, "callback_url": a.oauth.Gitee.CallbackURL()})
}
func (a *API) authorizeGitee(w http.ResponseWriter, r *http.Request) {
	state, nonce := identity.NewToken(), identity.NewToken()
	target, err := a.oauth.Gitee.Start(r.Context(), browserFrom(r).token, state, nonce)
	if err != nil {
		giteeError(w, err)
		return
	}
	// At most one browser authorization flow is active. The shared callback can
	// distinguish the dedicated repository flow without trusting query input.
	expired := identity.FlowCookie("")
	expired.MaxAge = -1
	http.SetCookie(w, expired)
	http.SetCookie(w, giteeCookie(nonce))
	w.Header().Set("Referrer-Policy", "no-referrer")
	writeJSON(w, 200, map[string]string{"authorization_url": target})
}
func (a *API) finishGitee(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	query, err := url.ParseQuery(r.URL.RawQuery)
	flows := r.CookiesNamed(giteeCookieName)
	sessions := r.CookiesNamed(identity.CookieName)
	if err != nil || len(query["state"]) != 1 || len(query["code"]) > 1 || len(query["error"]) > 1 || len(flows) != 1 || len(sessions) != 1 || len(r.Header.Values("Authorization")) != 0 {
		giteeError(w, identity.ErrOAuthFlow)
		return
	}
	code := query.Get("code")
	if len(query["error"]) != 0 {
		code = ""
	}
	err = a.oauth.Gitee.Finish(r.Context(), sessions[0].Value, query.Get("state"), flows[0].Value, code)
	expired := giteeCookie("")
	expired.MaxAge = -1
	http.SetCookie(w, expired)
	if err != nil {
		giteeError(w, err)
		return
	}
	http.Redirect(w, r, "/settings/repositories", http.StatusSeeOther)
}
func (a *API) revokeGitee(w http.ResponseWriter, r *http.Request) {
	if err := a.oauth.Gitee.Store.RevokeGiteeGrant(r.Context(), browserFrom(r).token); err != nil {
		giteeError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *API) giteeRepositories(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	page := 1
	if err != nil || len(query["page"]) > 1 {
		giteeError(w, gitee.ErrStale)
		return
	}
	if query.Has("page") {
		page, err = strconv.Atoi(query.Get("page"))
		if err != nil || page < 1 || page > 100 {
			giteeError(w, gitee.ErrStale)
			return
		}
	}
	result, err := a.oauth.Gitee.Repositories(r.Context(), browserFrom(r).token, page)
	if err != nil {
		giteeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *API) importGitee(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Repositories []gitee.Selection `json:"repositories"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		return
	}
	result, err := a.oauth.Gitee.Import(r.Context(), browserFrom(r).token, input.Repositories)
	if err != nil {
		giteeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": result})
}
func giteeError(w http.ResponseWriter, err error) {
	if accessError(w, err) {
		return
	}
	switch {
	case errors.Is(err, scm.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		writeProblem(w, 429, "Gitee rate limited", "Gitee 请求受限，请稍后重试。")
	case errors.Is(err, scm.ErrUnauthorized):
		writeProblem(w, 403, "Gitee access denied", "需要已绑定管理员的 Gitee 仓库授权和仓库管理权限。")
	case errors.Is(err, gitee.ErrStale), errors.Is(err, gitee.ErrBusy):
		writeProblem(w, 409, "Gitee authorization changed", "授权已变化、失效或正在刷新，请稍后重试，必要时重新授权。")
	case errors.Is(err, identity.ErrOAuthFlow):
		writeProblem(w, 400, "invalid authorization", "授权流程无效或过期，请在同一浏览器重新授权。")
	default:
		writeProblem(w, 502, "Gitee unavailable", "无法完成 Gitee 请求，请检查服务连接后重试。")
	}
}
