//go:build darwin && !cgo

package executor

import "syscall"

// Repository builds require CGO. Do not reinstate optimistic kqueue evidence in
// standalone executor builds that omit the native waitid binding.
func observeDarwinExit(int) (bool, error) {
	return false, syscall.ENOSYS
}
