package executor

import (
	"fmt"
	"strings"
)

// ClassifyNonZeroExit determines whether a non-zero exit code is an error.
// Shell exit 1 with stdout is a soft failure (e.g., grep no matches).
func ClassifyNonZeroExit(language Language, exitCode int, stdout, stderr string) ExitClassification {
	isSoftFail := language == Shell && exitCode == 1 && strings.TrimSpace(stdout) != ""
	if isSoftFail {
		return ExitClassification{IsError: false, Output: stdout}
	}
	return ExitClassification{
		IsError: true,
		Output:  fmt.Sprintf("Exit code: %d\n\nstdout:\n%s\n\nstderr:\n%s", exitCode, stdout, stderr),
	}
}
