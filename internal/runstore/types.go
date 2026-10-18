// Package runstore persists captured command output in a separate encrypted
// owner-local SQLite database. It never opens the knowledge store or vault.
package runstore

import (
	"time"

	"github.com/serpro69/capy/internal/executor"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func failure(code, message string) error { return &Error{Code: code, Message: message} }

type Capture struct {
	State               string  `json:"state"`
	ObservedBytes       int64   `json:"observed_bytes"`
	CapturedBytes       int64   `json:"captured_bytes"`
	Reason              *string `json:"reason"`
	LastCommittedCursor *string `json:"last_committed_cursor"`
	TerminalPersisted   bool    `json:"terminal_persisted"`
}
type Projection struct {
	Version        int     `json:"version"`
	State          string  `json:"state"`
	CoverageCursor *string `json:"coverage_cursor"`
}
type Index struct {
	State          string  `json:"state"`
	CoverageCursor *string `json:"coverage_cursor"`
	Error          *Error  `json:"error,omitempty"`
}

type Result struct {
	Capacity        *Capacity  `json:"capacity,omitempty"`
	Preview         string     `json:"preview,omitempty"`
	MetadataOmitted bool       `json:"metadata_omitted,omitempty"`
	Version         int        `json:"version"`
	Kind            string     `json:"kind"`
	RunID           *string    `json:"run_id"`
	OwnerID         string     `json:"owner_id,omitempty"`
	OwnerType       string     `json:"owner_type,omitempty"`
	State           string     `json:"state"`
	ExitCode        *int       `json:"exit_code"`
	Signal          *int       `json:"signal"`
	Reason          *string    `json:"reason"`
	StartedAt       *time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	ElapsedMS       int64      `json:"elapsed_ms"`
	Capture         Capture    `json:"capture"`
	Redaction       Projection `json:"redaction"`
	Index           Index      `json:"index"`
	ExpiresAt       *time.Time `json:"expires_at"`
	Error           *Error     `json:"error"`
}

type ActiveRun struct {
	RunID       string `json:"run_id"`
	AgeMS       int64  `json:"age_ms"`
	RemainingMS *int64 `json:"remaining_ms"`
}

type Capacity struct {
	MaxActive    int         `json:"max_active"`
	ActiveCount  int         `json:"active_count"`
	PendingCount int         `json:"pending_count"`
	Runs         []ActiveRun `json:"runs"`
	Omitted      bool        `json:"omitted,omitempty"`
	ListAction   string      `json:"list_action"`
	CancelAction string      `json:"cancel_action"`
}

func Rejection(code, message string) Result {
	return Result{Version: 1, Kind: "rejection", State: "not_started", Capture: Capture{State: "not_started"},
		Redaction: Projection{Version: 1, State: "unavailable"}, Index: Index{State: "not_requested"}, Error: &Error{Code: code, Message: message}}
}

type Program struct {
	OwnerType   string               `json:"owner_type,omitempty"`
	Owner       string               `json:"owner"`
	Request     executor.ExecRequest `json:"request"`
	Intent      string               `json:"intent"`
	Deadline    time.Time            `json:"deadline"`
	TTLHours    int                  `json:"ttl_hours"`
	MaxRunBytes int64                `json:"max_run_bytes"`
}

type Admitted struct {
	ID, Generation string
	Slot           int
	Program        Program
	Result         Result
	Cleanup        string
}

type Completion struct {
	SpawnCode                     int
	Started, Retired, CaptureLost bool
	ExitCode                      *int
	Signal                        int
	Reason, CaptureReason         string
	ObservedBytes, ElapsedMS      int64
}

type Fragment struct {
	Sequence   uint64 `json:"sequence"`
	Phase      string `json:"phase"`
	Stream     string `json:"stream"`
	ByteOffset int64  `json:"byte_offset"`
	StartLine  int64  `json:"start_line"`
	Data       []byte `json:"data,omitempty"`
	Text       string `json:"text,omitempty"`
	RawEnd     int64  `json:"raw_end,omitempty"`
	Redacted   bool   `json:"redacted,omitempty"`
	Continued  bool   `json:"continued,omitempty"`
}

type ReadPage struct {
	RedactionPending bool       `json:"redaction_pending,omitempty"`
	Version          int        `json:"version"`
	Kind             string     `json:"kind"`
	RunID            string     `json:"run_id"`
	Raw              bool       `json:"raw"`
	Encoding         string     `json:"encoding"`
	Fragments        []Fragment `json:"fragments"`
	NextCursor       *string    `json:"next_cursor"`
	EndOfAvailable   bool       `json:"end_of_available"`
	EOF              bool       `json:"eof"`
	Run              Result     `json:"run"`
}
