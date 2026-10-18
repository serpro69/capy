package hook

import (
	"fmt"
	"strings"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/platform"
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
		return a.FormatBlock(routineCaptureReason)
	}
	// The existing HTTP safety splitter recognizes semicolons. Preserve leaf
	// boundaries so a safe sibling's download flags cannot exempt unsafe stdout.
	return routeBashOutput(strings.Join(inspection.NativeCommands, "; "), event, a, ctx)
}
