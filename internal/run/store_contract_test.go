package run_test

import (
	runmodel "github.com/yuanci/yuanci/internal/run"
	"github.com/yuanci/yuanci/internal/run/storetest"
	"testing"
)

func TestMemoryStoreContract(t *testing.T) {
	storetest.Exercise(t, func(t *testing.T) runmodel.Store {
		store := runmodel.NewMemoryStore()
		t.Cleanup(store.Close)
		return store
	})
}
