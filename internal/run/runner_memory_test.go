package run_test

import (
	"testing"

	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/run/storetest"
)

func TestMemoryRunnerStoreContract(t *testing.T) {
	storetest.ExerciseRunner(t, func(t *testing.T, runner runmodel.RunnerDescriptor) runmodel.RunnerJobStore {
		store := runmodel.NewMemoryStore()
		if _, err := store.RenewRunnerLeases(t.Context(), runmodel.HeartbeatRequest{Runner: runner}); err != nil {
			t.Fatal(err)
		}
		return store
	})
}
