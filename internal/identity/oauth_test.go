package identity

import "testing"

func TestPKCEAndSubjectBinding(t *testing.T) {
	state, nonce := NewToken(), NewToken()
	verifier := PKCEVerifier(state, nonce)
	if len(verifier) != 43 || len(PKCEChallenge(verifier)) != 43 || verifier == PKCEChallenge(verifier) {
		t.Fatal("invalid PKCE")
	}
	if verifier == PKCEVerifier(state, NewToken()) || verifier == PKCEVerifier(NewToken(), nonce) {
		t.Fatal("unbound PKCE")
	}
	for _, bad := range []string{"", "0", "-1", "01", "+1", "1.0", "user-name"} {
		if ValidGitHubSubject(bad) {
			t.Fatal("noncanonical subject accepted")
		}
	}
	if !ValidGitHubSubject("12345") {
		t.Fatal("valid subject rejected")
	}
	cookie := FlowCookie(nonce)
	if !cookie.Secure || !cookie.HttpOnly || cookie.Domain != "" || cookie.Path != "/" || cookie.MaxAge != 300 {
		t.Fatal("unsafe flow cookie")
	}
}

func TestExternalUserValidationIsScopedToProviderAndInstance(t *testing.T) {
	valid := ExternalUser{Provider: "gitee", Instance: GiteeInstance, Subject: "42", Login: "fixture"}
	if !valid.Valid() {
		t.Fatal("valid Gitee identity rejected")
	}
	valid.Instance = GitHubInstance
	if valid.Valid() {
		t.Fatal("Gitee subject accepted for the GitHub instance")
	}
	valid.Provider = "unknown"
	valid.Instance = "https://example.test"
	if valid.Valid() {
		t.Fatal("unknown provider accepted")
	}
}
