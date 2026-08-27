package run

import "testing"

func TestRunTransitions(t *testing.T) {
	if err := ValidateTransition(StatusQueued, StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTransition(StatusSucceeded, StatusRunning); err == nil {
		t.Fatal("terminal run must not restart")
	}
	if !StatusFailed.Terminal() || StatusRunning.Terminal() {
		t.Fatal("terminal classification is incorrect")
	}
}

func TestJobCanReturnToQueueAfterAssignmentFailure(t *testing.T) {
	if err := ValidateJobTransition(JobAssigned, JobQueued); err != nil {
		t.Fatal(err)
	}
}
