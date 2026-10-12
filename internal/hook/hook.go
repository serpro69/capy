package hook

import (
	"fmt"
	"io"
	"os"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/security"
)

// Run dispatches a hook event by reading JSON from stdin, routing to the
// appropriate handler, and writing the response JSON to stdout.
func Run(event string, a adapter.HookAdapter, explicitProjectDir *string) error {
	if err := validateEvent(event, a.Platform()); err != nil {
		return err
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}

	output, err := handleEvent(event, input, a, explicitProjectDir)
	if err != nil {
		return err
	}
	if output != nil {
		_, err = os.Stdout.Write(output)
	}
	return err
}

// handleEvent selects one project before loading any policies or mutating state.
func handleEvent(event string, input []byte, a adapter.HookAdapter, explicitProjectDir *string) ([]byte, error) {
	if err := validateEvent(event, a.Platform()); err != nil {
		return nil, err
	}
	parsed, parseErr := a.ParsePreToolUse(input)
	if parseErr != nil {
		if a.Platform() == adapter.Codex {
			if event == "pretooluse" {
				return a.FormatBlock("capy: invalid Codex PreToolUse payload; expected valid hook JSON")
			}
			return nil, parseErr
		}
		parsed = &adapter.PreToolUseEvent{}
	}
	ctx, err := resolveHookContext(a.Platform(), explicitProjectDir, parsed)
	if err != nil {
		if event == "pretooluse" {
			return a.FormatBlock(err.Error())
		}
		return nil, err
	}
	switch event {
	case "pretooluse":
		if parseErr != nil {
			return nil, nil // retain malformed-input passthrough
		}
		policies := security.ReadBashPolicies(ctx.projectDir, "")
		return routePreToolUse(parsed, a, policies, ctx)
	case "posttooluse":
		return handlePostToolUse(input, parsed, ctx)
	case "precompact":
		return handlePreCompact(input, a)
	case "sessionstart":
		return handleSessionStart(input, a)
	case "subagentstart":
		return a.FormatSubagentStart(CodexRoutingBlock())
	case "userpromptsubmit":
		return handleUserPromptSubmit(input, a)
	case "sessionend":
		handleSessionEnd(ctx.projectDir, parsed)
		return nil, nil // no output, no error — best effort
	default:
		return nil, fmt.Errorf("unknown hook event: %s", event)
	}
}

func validateEvent(event string, platform adapter.Platform) error {
	switch platform {
	case adapter.ClaudeCode:
		switch event {
		case "pretooluse", "posttooluse", "precompact", "sessionstart", "userpromptsubmit", "sessionend":
			return nil
		}
		return fmt.Errorf("unknown hook event: %s", event)
	case adapter.Codex:
		switch event {
		case "pretooluse", "sessionstart", "subagentstart", "posttooluse", "sessionend":
			return nil
		}
	default:
		return fmt.Errorf("unknown hook platform %q", platform)
	}
	return fmt.Errorf("unsupported hook event %q for platform %q", event, platform)
}
