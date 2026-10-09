package gitee

import (
	"encoding/json"
	"testing"
)

func TestCheckRunListEnvelope(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		count int
		valid bool
	}{
		{"empty envelope", `{"total_count":0,"check_runs":[]}`, 0, true},
		{"envelope", `{"total_count":1,"check_runs":[{"id":19,"name":"YuanCI/run"}]}`, 1, true},
		{"legacy array", `[{"id":19,"name":"YuanCI/run"}]`, 1, true},
		{"missing field", `{}`, 0, false},
		{"null", `{"check_runs":null}`, 0, false},
		{"wrong shape", `{"check_runs":{}}`, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var checks checkRunList
			err := json.Unmarshal([]byte(test.body), &checks)
			if (err == nil) != test.valid || (err == nil && len(checks) != test.count) {
				t.Fatalf("count=%d error=%v", len(checks), err)
			}
		})
	}
}
