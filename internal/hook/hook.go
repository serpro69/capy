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
func Run(event string, a adapter.HookAdapter, policies []security.SecurityPolicy, projectDir string) error {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}

	var output []byte
	switch event {
	case "pretooluse":
		output, err = handlePreToolUse(input, a, policies, projectDir)
	case "posttooluse":
		output, err = handlePostToolUse(input, a)
	case "precompact":
		output, err = handlePreCompact(input, a)
	case "sessionstart":
		output, err = handleSessionStart(input, a)
	case "userpromptsubmit":
		output, err = handleUserPromptSubmit(input, a)
	case "sessionend":
		handleSessionEnd(projectDir)
		return nil // no output, no error — best effort
	default:
		return fmt.Errorf("unknown hook event: %s", event)
	}

	if err != nil {
		return err
	}
	if output != nil {
		_, err = os.Stdout.Write(output)
	}
	return err
}
