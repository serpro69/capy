//go:build !linux

package execution

import (
	"context"
	"errors"
)

func ServeInherited(context.Context, OpenRun) error {
	return errors.New("run supervision is supported only on Linux")
}
