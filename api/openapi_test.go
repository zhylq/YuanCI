package api

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPIReferencesAndBrowserSecurity(t *testing.T) {
	source, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(source, &document); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			for key, child := range node {
				if key == "$ref" {
					ref, ok := child.(string)
					if !ok || !strings.HasPrefix(ref, "#/") {
						t.Fatalf("unexpected reference: %v", child)
					}
					var target any = document
					for _, segment := range strings.Split(ref[2:], "/") {
						segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
						mapping, ok := target.(map[string]any)
						if !ok {
							t.Fatalf("invalid reference: %s", ref)
						}
						target, ok = mapping[segment]
						if !ok {
							t.Fatalf("missing reference: %s", ref)
						}
					}
				} else {
					walk(child)
				}
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(document)
	components := document["components"].(map[string]any)
	cookie := components["securitySchemes"].(map[string]any)["cookieSession"].(map[string]any)
	if cookie["name"] != "__Host-yuanci_session" || cookie["in"] != "cookie" {
		t.Fatal("cookie contract drift")
	}
	setupCookie := components["securitySchemes"].(map[string]any)["setupSession"].(map[string]any)
	if setupCookie["name"] != "__Host-yuanci_setup" || setupCookie["in"] != "cookie" {
		t.Fatal("setup cookie contract drift")
	}
	candidate := components["requestBodies"].(map[string]any)["LoginCandidate"].(map[string]any)
	schema := candidate["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	secret := schema["properties"].(map[string]any)["client_secret"].(map[string]any)
	if secret["writeOnly"] != true {
		t.Fatal("login secret must be write-only")
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/projects", "/projects/{projectID}", "/projects/{projectID}/runs"} {
		entry, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing project contract %s", path)
		}
		operation := entry["get"].(map[string]any)
		if _, override := operation["security"]; override {
			t.Fatal("project reads must inherit browser session security")
		}
		if len(entry) != 1 {
			t.Fatal("project browser must remain read-only")
		}
	}
	for _, name := range []string{"Project", "ProjectRunSummary"} {
		schema := components["schemas"].(map[string]any)[name].(map[string]any)
		if schema["additionalProperties"] != false {
			t.Fatal("project DTO must be explicit")
		}
		properties := schema["properties"].(map[string]any)
		for _, forbidden := range []string{"clone_url", "plan", "encrypted_secret", "token", "total"} {
			if _, exists := properties[forbidden]; exists {
				t.Fatal("project read contract exposes sensitive field")
			}
		}
	}
	for _, path := range []string{"/auth/github/start", "/auth/github/callback"} {
		entry, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing login contract %s", path)
		}
		operation := entry["get"].(map[string]any)
		security, ok := operation["security"].([]any)
		if !ok || len(security) != 0 {
			t.Fatalf("login must not require an existing session: %s", path)
		}
	}
	if _, ok := paths["/auth/github/link"]; !ok {
		t.Fatal("missing identity link contract")
	}
	for path, raw := range paths {
		for method, rawOperation := range raw.(map[string]any) {
			if method != "post" && method != "delete" && method != "put" && method != "patch" {
				continue
			}
			operation := rawOperation.(map[string]any)
			parameters, _ := operation["parameters"].([]any)
			refs := map[string]bool{}
			for _, parameter := range parameters {
				ref, _ := parameter.(map[string]any)["$ref"].(string)
				refs[ref] = true
			}
			if path == "/setup/exchange" && method == "post" {
				security, ok := operation["security"].([]any)
				if !refs["#/components/parameters/BrowserOrigin"] || !ok || len(security) != 0 {
					t.Fatal("setup exchange must require Origin and a one-time code, not a session")
				}
				continue // Only this code-authenticated operation has no existing CSRF session.
			}
			if !refs["#/components/parameters/BrowserOrigin"] || !refs["#/components/parameters/CSRFToken"] {
				t.Errorf("%s %s missing CSRF/origin contract", method, path)
			}
		}
	}
}
