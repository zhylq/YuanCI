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
	paths := document["paths"].(map[string]any)
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
			if !refs["#/components/parameters/BrowserOrigin"] || !refs["#/components/parameters/CSRFToken"] {
				t.Errorf("%s %s missing CSRF/origin contract", method, path)
			}
		}
	}
}
