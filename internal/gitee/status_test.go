package gitee

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/yuanci/yuanci/internal/commitstatus"
)

func TestCheckRunDeliveryContract(t *testing.T) {
	for _, state := range []commitstatus.State{commitstatus.StatePending, commitstatus.StateSuccess, commitstatus.StateFailure, commitstatus.StateError} {
		t.Run(string(state), func(t *testing.T) {
			c := NewClient()
			calls := 0
			item := commitstatus.Item{RunID: uuid.New(), CommitSHA: strings.Repeat("a", 40), State: state, Description: "Run result"}
			c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Authorization") != "" || r.URL.Query().Get("access_token") != "private-token" {
					t.Fatal("credential transport")
				}
				if calls == 1 {
					if r.Method != "GET" || r.URL.Query().Get("check_name") != "YuanCI/"+item.RunID.String() {
						t.Fatal("missing deterministic name")
					}
					return response(r, 200, `[{"id":19,"name":"YuanCI/`+item.RunID.String()+`","head_sha":"`+item.CommitSHA+`","status":"queued"}]`), nil
				}
				if r.Method != "PATCH" || !strings.HasSuffix(r.URL.Path, "/check-runs/19") {
					t.Fatal("must reuse check")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Fatal("body")
				}
				expected := "completed"
				if state == commitstatus.StatePending {
					expected = "queued"
				}
				if body["status"] != expected {
					t.Fatal("state mapping")
				}
				return response(r, 200, `{}`), nil
			})
			if err := c.DeliverCheck(t.Context(), "private-token", Repository{Owner: "owner", Name: "repo"}, item, "https://ci.test/runs/"+item.RunID.String()); err != nil || calls != 2 {
				t.Fatalf("deliver %v calls %d", err, calls)
			}
		})
	}
}

func TestCheckRunNeverRegressesCompleted(t *testing.T) {
	c := NewClient()
	item := commitstatus.Item{RunID: uuid.New(), CommitSHA: strings.Repeat("b", 40), State: commitstatus.StatePending}
	c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("regressed final state")
		}
		return response(r, 200, `[{"id":20,"name":"YuanCI/`+item.RunID.String()+`","head_sha":"`+item.CommitSHA+`","status":"completed"}]`), nil
	})
	if err := c.DeliverCheck(t.Context(), "token", Repository{Owner: "owner", Name: "repo"}, item, "https://ci.test"); err != nil {
		t.Fatal(err)
	}
	item.CommitSHA = "main"
	if err := c.DeliverCheck(t.Context(), "token", Repository{Owner: "owner", Name: "repo"}, item, "https://ci.test"); err == nil {
		t.Fatal("mutable status target")
	}
}
