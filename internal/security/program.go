package security

// EvaluateProgram applies the selected owner's policy to a captured program,
// including literal nested capy invocations. A nested --project-dir never
// replaces the outer policy. Code-file captures inside programs fail closed:
// their runtime launch directory and environment cannot be established here.
// denyOnly preserves MCP's existing approval semantics.
func EvaluateProgram(code, language string, policies []SecurityPolicy, denyOnly bool) (CommandDecision, error) {
	// The shell scanner has already budgeted nested substitution visits; the
	// wrapper pass charges both each emitted spelling and its decoded suffix.
	// Allow those bounded passes without rejecting existing scanner-limit inputs.
	i := routineInspector{remaining: 24*len(code) + 4096}
	if language == "shell" {
		if err := i.shell(code, true, 0); err != nil {
			return CommandDecision{}, err
		}
	} else {
		for _, command := range ExtractEmbeddedCommands(code, language) {
			text := command.text
			if command.argv != nil {
				text = joinArgs(command.argv)
				i.policy = append(i.policy, command.text)
			}
			if err := i.shell(text, true, 0); err != nil {
				return CommandDecision{}, err
			}
		}
	}
	if denyOnly {
		return evaluateSegmentsDenyOnly(i.policy, policies), nil
	}
	return evaluateSegments(i.policy, policies), nil
}
