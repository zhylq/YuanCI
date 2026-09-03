package commitstatus

import "testing"

func TestStateValidation(t *testing.T) {
	for _, state := range []State{StatePending, StateSuccess, StateFailure, StateError} {
		if !state.Valid() {
			t.Fatalf("valid state %q was rejected", state)
		}
	}
	if State("queued").Valid() {
		t.Fatal("delivery state was accepted as provider state")
	}
}
