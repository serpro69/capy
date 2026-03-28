package server

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// StartLifecycleGuard monitors for parent process death and OS signals,
// calling onShutdown when the server should exit. Returns a cleanup function
// that must be called to stop the guard.
func StartLifecycleGuard(onShutdown func()) func() {
	originalPpid := os.Getppid()
	var once sync.Once

	shutdown := func() {
		once.Do(onShutdown)
	}

	// Parent PID polling (every 30s) — detect orphaned server
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		for range ticker.C {
			ppid := os.Getppid()
			if ppid != originalPpid || ppid == 0 || ppid == 1 {
				shutdown()
				return
			}
		}
	}()

	// OS signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			shutdown()
		case <-done:
		}
	}()

	// Return cleanup function
	return func() {
		close(done)
		ticker.Stop()
		signal.Stop(sigCh)
	}
}
