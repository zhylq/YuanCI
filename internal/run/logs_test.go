package run

import (
	"github.com/google/uuid"
	"testing"
)

func TestValidateLogChunkBounds(t *testing.T) {
	base := LogChunk{Lease: LeaseRequest{RunnerID: uuid.New(), JobID: uuid.New(), LeaseToken: "lease"}, Sequence: 1, Stream: "stdout", Data: []byte("data")}
	if err := ValidateLogChunk(base); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*LogChunk){
		"sequence zero":     func(c *LogChunk) { c.Sequence = 0 },
		"sequence overflow": func(c *LogChunk) { c.Sequence = MaxJobLogChunks + 1 },
		"step":              func(c *LogChunk) { c.Step = 1024 },
		"negative step":     func(c *LogChunk) { c.Step = -1 },
		"stream":            func(c *LogChunk) { c.Stream = "other" },
		"empty":             func(c *LogChunk) { c.Data = nil },
		"oversized":         func(c *LogChunk) { c.Data = make([]byte, MaxLogChunkBytes+1) },
		"marker payload":    func(c *LogChunk) { c.Truncated = true },
		"runner":            func(c *LogChunk) { c.Lease.RunnerID = uuid.Nil },
		"token":             func(c *LogChunk) { c.Lease.LeaseToken = "" },
	} {
		t.Run(name, func(t *testing.T) {
			c := base
			mutate(&c)
			if ValidateLogChunk(c) == nil {
				t.Fatal("accepted invalid chunk")
			}
		})
	}
	base.Data = nil
	base.Truncated = true
	if err := ValidateLogChunk(base); err != nil {
		t.Fatal(err)
	}
}
