package hook

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/platform"
	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/security"
)

const routineCaptureReason = "capy: Routine command capture is required. Nothing in this request ran. " +
	"Use capy_execute(language, code) or capy_batch_execute; capy execute --code '<program>' is a CLI capture alternative. " +
	"Availability is not assumed. If capture cannot start, stop and repair it; do not run the command natively. " +
	"If execution may have started, recover with capy_runs or capy runs before any retry. " +
	"Only explicit routing.routine operator exceptions permit native routine execution."

// routeCodexBash applies whole-request security, then output policy. It must
// return before HTTP routing can consume observations or publish fallback advice.
// It deliberately does not use execution/storage services or credential APIs.
func routeCodexBash(command string, event *adapter.PreToolUseEvent, policies []security.SecurityPolicy, ctx hookContext, a adapter.HookAdapter) ([]byte, error) {
	inspection, err := security.InspectRoutineCommand(command, security.RoutineOptions{
		Policies: policies,
		ReadCodeFile: func(inv security.CaptureInvocation) (string, error) {
			owner, err := platform.ResolveRunOwner(ctx.workingDir, inv.ProjectDir)
			if err != nil {
				return "", fmt.Errorf("resolve capture CLI owner: %w", err)
			}
			return security.ReadCLIProgram(owner, inv.CodeFile)
		},
	})
	if err != nil {
		return a.FormatBlock(fmt.Sprintf("Command blocked: %v", err))
	}
	if inspection.Security.Decision == "deny" {
		return formatPolicyDeny(inspection.Security, a)
	}
	if inspection.Security.Decision == "ask" && inspection.Security.MatchedPattern != "" {
		return a.FormatAsk(policyAskReason(inspection.Security))
	}
	cfg, err := config.Load(ctx.projectDir)
	if err != nil {
		return a.FormatBlock("capy: invalid routing configuration; repair the selected owner's capy configuration before retrying")
	}
	if cfg.Routing.Routine.Enabled && inspection.RequiresCapture(cfg.Routing.Routine.NativeAllow) {
		if ctx.hasPayloadCwd && len(event.ToolInput) == 1 && len(inspection.Captures) == 0 {
			return a.FormatBlock(routineRetryReason(command, ctx.workingDir))
		}
		return a.FormatBlock(routineCaptureReason)
	}
	// The existing HTTP safety splitter recognizes semicolons. Preserve leaf
	// boundaries so a safe sibling's download flags cannot exempt unsafe stdout.
	return routeBashOutput(strings.Join(inspection.NativeCommands, "; "), event, a, ctx)
}

func routineRetryReason(command, cwd string) string {
	// Never truncate or redact an executable suggestion. Withhold the example
	// if it would publish protected text or exceed the denial reason's budget.
	const maxRetryBytes = 4096
	if len(command)+len(cwd) > maxRetryBytes || !utf8.ValidString(cwd) {
		return routineCaptureReason
	}
	// Assignments can carry arbitrary credentials, and URLs can include userinfo.
	if strings.Contains(command, "=") || strings.Contains(command, "://") {
		return routineCaptureReason
	}
	// The newline terminates partial redaction prefixes at the field boundary.
	// Check the original strings before JSON escapes private tags.
	spans, err := sanitize.NewStream().Feed([]byte(command+"\n"+cwd+"\n"), true)
	if err != nil {
		return routineCaptureReason
	}
	for _, span := range spans {
		if span.Redacted {
			return routineCaptureReason
		}
	}
	args, err := json.Marshal(map[string]string{"language": "shell", "code": command, "cwd": cwd})
	if err != nil {
		return routineCaptureReason
	}
	reason := "capy: Routine command capture is required. Nothing in this request ran. Honor any other denial or approval requirement. For an authorized local shell command in the directory below, retry with capy_execute " + string(args) +
		". If capture cannot start, stop and repair it. If execution may have started, recover with capy_runs before retrying."
	if len(reason) > maxRetryBytes {
		return routineCaptureReason
	}
	return reason
}
