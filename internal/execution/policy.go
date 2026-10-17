package execution

import (
	"fmt"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/security"
)

// CheckCLIProgram enforces explicit denies and matched asks before admission.
// An unmatched default ask remains allowed, following the capture CLI contract.
func CheckCLIProgram(language executor.Language, code string, policies []security.SecurityPolicy) error {
	decisions := []security.CommandDecision{}
	if language == executor.Shell {
		decision, err := security.EvaluateCommand(code, policies)
		if err != nil {
			return &runstore.Error{Code: "permission_denied", Message: "program cannot be checked by the bounded shell policy scanner"}
		}
		decisions = append(decisions, decision)
	} else {
		for _, command := range security.ExtractEmbeddedCommands(code, string(language)) {
			decision, err := security.EvaluateEmbeddedCommand(command, policies)
			if err != nil {
				return &runstore.Error{Code: "permission_denied", Message: "embedded shell command cannot be checked by the bounded policy scanner"}
			}
			decisions = append(decisions, decision)
		}
	}
	for _, kind := range []string{"deny", "ask"} {
		for _, decision := range decisions {
			if decision.Decision != kind || decision.MatchedPattern == "" {
				continue
			}
			code := "permission_denied"
			message := "blocked by security policy"
			if kind == "ask" {
				code = "approval_required"
				message = "CLI cannot request approval"
			}
			return &runstore.Error{Code: code, Message: fmt.Sprintf("%s: matched %s rule %q from %q", message, kind, decision.MatchedPattern, decision.SourcePath)}
		}
	}
	return nil
}

func CLIExitCode(result runstore.Result) int {
	if result.State == "timed_out" {
		return 124
	}
	if result.State == "start_failed" {
		if result.Error != nil && result.Error.Code == "runtime_unavailable" {
			return 127
		}
		return 126
	}
	if result.Signal != nil {
		return 128 + *result.Signal
	}
	if result.State == "exited" && result.ExitCode != nil {
		if *result.ExitCode != 0 {
			return *result.ExitCode
		}
		if result.Capture.State != "complete" {
			return 74
		}
		return 0
	}
	return 1
}
