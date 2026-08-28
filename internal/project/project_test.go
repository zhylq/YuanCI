package project

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorAndQueryBounds(t *testing.T) {
	id, stamp := uuid.New(), time.Now().UTC().Truncate(time.Microsecond)
	got, gotID, err := RunCursor(EncodeRunCursor(stamp, id))
	if err != nil || gotID != id || !got.Equal(stamp) {
		t.Fatal("cursor roundtrip")
	}
	for _, bad := range []string{"invalid", strings.Repeat("a", 161), "AA==", "MjAyMC0wMS0wMVQwMDowMDowMFp8"} {
		if _, _, err := RunCursor(bad); err == nil {
			t.Fatal("invalid run cursor accepted")
		}
	}
	for _, bad := range []string{"bad", uuid.Nil.String(), strings.ReplaceAll(id.String(), "-", "")} {
		if _, err := ProjectCursor(bad); err == nil {
			t.Fatal("invalid project cursor accepted")
		}
	}
	for _, q := range []Query{{Limit: 0}, {Limit: 101}, {Limit: 20, Search: strings.Repeat("a", 101)}, {Limit: 20, Search: "\x00"}, {Limit: 20, Search: "\xff"}} {
		if q.Validate() == nil {
			t.Fatal("unbounded query accepted")
		}
	}
	if err := (Query{Limit: 100, Search: "%_仓库"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func FuzzRunCursor(f *testing.F) {
	f.Add("")
	f.Add("invalid")
	f.Add(EncodeRunCursor(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), uuid.New()))
	f.Fuzz(func(t *testing.T, value string) {
		stamp, id, err := RunCursor(value)
		if err == nil && value != "" {
			timeAgain, idAgain, err := RunCursor(EncodeRunCursor(stamp, id))
			if err != nil || idAgain != id || !timeAgain.Equal(stamp) {
				t.Fatal("unstable cursor")
			}
		}
	})
}
