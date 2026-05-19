package executor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

var benchLanguages = []struct {
	lang Language
	bin  string
	code string
}{
	{Shell, "bash", `echo "hello"`},
	{JavaScript, "node", `console.log("hello")`},
	{Python, "python3", `print("hello")`},
}

func BenchmarkExecutorOverhead(b *testing.B) {
	for _, bl := range benchLanguages {
		b.Run(string(bl.lang), func(b *testing.B) {
			if _, err := exec.LookPath(bl.bin); err != nil {
				b.Skipf("%s not available", bl.bin)
			}
			e := NewExecutor(b.TempDir(), MaxOutputBytes)
			ctx := context.Background()
			for b.Loop() {
				if _, err := e.Execute(ctx, ExecRequest{
					Language:   bl.lang,
					Code:       bl.code,
					TimeoutSec: 10,
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkExecutorOverheadParallel(b *testing.B) {
	for _, bl := range benchLanguages {
		b.Run(string(bl.lang), func(b *testing.B) {
			if _, err := exec.LookPath(bl.bin); err != nil {
				b.Skipf("%s not available", bl.bin)
			}
			e := NewExecutor(b.TempDir(), MaxOutputBytes)
			ctx := context.Background()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := e.Execute(ctx, ExecRequest{
						Language:   bl.lang,
						Code:       bl.code,
						TimeoutSec: 10,
					}); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}

func BenchmarkExecutorScaling(b *testing.B) {
	if _, err := exec.LookPath("bash"); err != nil {
		b.Skip("bash not available")
	}

	sizes := []struct {
		name string
		kb   int
	}{
		{"1KB", 1},
		{"10KB", 10},
		{"100KB", 100},
		{"1MB", 1024},
	}

	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			e := NewExecutor(b.TempDir(), MaxOutputBytes)
			ctx := context.Background()
			lineCount := (sz.kb * 1024) / 80
			code := fmt.Sprintf(`for i in $(seq 1 %d); do echo "benchmark output line $i padding%s"; done`, lineCount, strings.Repeat("x", 40))
			for b.Loop() {
				if _, err := e.Execute(ctx, ExecRequest{
					Language:   Shell,
					Code:       code,
					TimeoutSec: 30,
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSafeEnv(b *testing.B) {
	tmpDir := b.TempDir()
	for b.Loop() {
		_ = BuildSafeEnv(tmpDir)
	}
}

func BenchmarkProcessGroupKill(b *testing.B) {
	if _, err := exec.LookPath("bash"); err != nil {
		b.Skip("bash not available")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cmd := exec.Command("bash", "-c", "sleep 60")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		b.StopTimer()
		if err := cmd.Start(); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cmd.Wait()
	}
}
