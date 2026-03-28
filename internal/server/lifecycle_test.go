package server

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestStartLifecycleGuard_Cleanup(t *testing.T) {
	var called atomic.Bool
	stop := StartLifecycleGuard(func() {
		called.Store(true)
	})

	// Stop immediately — should not trigger shutdown
	stop()
	time.Sleep(50 * time.Millisecond)
	assert.False(t, called.Load(), "shutdown should not be called after cleanup")
}
