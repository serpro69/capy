//go:build linux || darwin

package execution

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// inheritedExchangeFiles owns exactly the existing FDs 3..7. Callers close any
// bound backend before closing these files; user commands must never inherit them.
func inheritedExchangeFiles() ([]*os.File, error) {
	files := make([]*os.File, 0, 5)
	for fd := 3; fd <= 7; fd++ {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			for _, f := range files {
				f.Close()
			}
			return nil, errors.New("missing exchange descriptor")
		}
		unix.CloseOnExec(fd)
		if fd <= 5 {
			if stat.Mode&unix.S_IFMT != unix.S_IFIFO {
				for _, f := range files {
					f.Close()
				}
				return nil, errors.New("exchange channel is not a pipe")
			}
			if err := unix.SetNonblock(fd, true); err != nil {
				for _, f := range files {
					f.Close()
				}
				return nil, err
			}
		}
		files = append(files, os.NewFile(uintptr(fd), "exchange-private"))
	}
	return files, nil
}

// acceptLeaseStartup authorizes no storage before descriptor validation and the
// common exchange. bind must authenticate/bind the admitted store and selected
// scope, and retain its backend until cleanup completes. Runtime/terminal handling
// follows in the platform supervisor; this function ends at start authorization.
func acceptLeaseStartup(ctx context.Context, files []*os.File, scope string, bind func(context.Context, exchangeStartup, string) error) (exchangeStartup, error) {
	var start exchangeStartup
	if len(files) != 5 || !exchangeScope(scope) || bind == nil {
		return start, errors.New("invalid exchange helper inputs")
	}
	input, credential, events := files[0], files[1], files[2]
	leases := Leases{Owner: files[3], Lifecycle: files[4]}
	if err := validateLeases(leases.Owner, leases.Lifecycle); err != nil {
		return start, err
	}
	deadline := time.Now().Add(startupTimeout)
	if d, ok := ctx.Deadline(); ok {
		deadline = minDeadline(deadline, d)
	}
	if err := input.SetReadDeadline(deadline); err != nil {
		return start, err
	}
	if err := readFrame(input, &start); err != nil {
		return start, err
	}
	if start.CleanupScope != scope {
		return start, errors.New("exchange backend scope mismatch")
	}
	deadline = minDeadline(deadline, start.Deadline)
	if err := credential.SetReadDeadline(deadline); err != nil {
		return start, err
	}
	var encoded []byte
	if err := readFrameLimit(credential, &encoded, maxCredentialFrameBytes); err != nil {
		return start, err
	}
	key := string(encoded)
	clear(encoded)
	if err := credential.Close(); err != nil {
		return start, err
	}
	if key == "" {
		return start, errors.New("missing exchange credential")
	}
	x := exchangeIO{input: input, output: events, id: start.ExchangeID, deadline: deadline}
	if err := observeLeaseExchange(x, leases); err != nil {
		return start, err
	}
	admissionCtx, cancel := context.WithDeadline(ctx, deadline)
	err := bind(admissionCtx, start, key)
	cancel()
	key = ""
	if err != nil {
		return start, errors.New("binding exchange backend")
	}
	if err := x.send(leaseMessage{Type: "ready", CleanupScope: scope}); err != nil {
		return start, err
	}
	authorization, err := x.receive("start", "", "", 0)
	if err != nil {
		return start, err
	}
	if authorization.CleanupScope != scope {
		return start, errors.New("exchange authorization scope mismatch")
	}
	if err := input.SetReadDeadline(time.Time{}); err != nil {
		return start, err
	}
	return start, nil
}
