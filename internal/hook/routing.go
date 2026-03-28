package hook

// RoutingBlock returns the XML routing instructions injected into subagent
// prompts and session start events. Guides the LLM to use capy MCP tools
// instead of raw Bash/Read/WebFetch.
func RoutingBlock() string {
	return `<context_window_protection>
  <priority_instructions>
    Raw tool output floods your context window. You MUST use capy
    MCP tools to keep raw data in the sandbox.
  </priority_instructions>

  <tool_selection_hierarchy>
    1. GATHER: capy_batch_execute(commands, queries)
       - Primary tool for research. Runs all commands, auto-indexes, and searches.
       - ONE call replaces many individual steps.
    2. FOLLOW-UP: capy_search(queries: ["q1", "q2", ...])
       - Use for all follow-up questions. ONE call, many queries.
    3. PROCESSING: capy_execute(language, code) | capy_execute_file(path, language, code)
       - Use for API calls, log analysis, and data processing.
  </tool_selection_hierarchy>

  <forbidden_actions>
    - DO NOT use Bash for commands producing >20 lines of output.
    - DO NOT use Read for analysis (use execute_file). Read IS correct for files you intend to Edit.
    - DO NOT use WebFetch (use capy_fetch_and_index instead).
    - Bash is ONLY for git/mkdir/rm/mv/navigation.
  </forbidden_actions>

  <output_constraints>
    <word_limit>Keep your final response under 500 words.</word_limit>
    <artifact_policy>
      Write artifacts (code, configs, PRDs) to FILES. NEVER return them as inline text.
      Return only: file path + 1-line description.
    </artifact_policy>
    <response_format>
      Your response must be a concise summary:
      - Actions taken (2-3 bullets)
      - File paths created/modified
      - Knowledge base source labels (so parent can search)
      - Key findings
    </response_format>
  </output_constraints>

  <capy_commands>
    When the user says "capy stats" or asks about context savings:
    → Call the capy_stats MCP tool and display the full output verbatim.

    When the user says "capy doctor" or asks to diagnose capy:
    → Call the capy_doctor MCP tool and display results as a checklist.
  </capy_commands>
</context_window_protection>`
}

// READ_GUIDANCE is the one-time advisory shown when Read is used.
const READ_GUIDANCE = `<context_guidance>
  <tip>
    If you are reading this file to Edit it, Read is the correct tool — Edit needs file content in context.
    If you are reading to analyze or explore, use capy_execute_file(path, language, code) instead — only your printed summary will enter the context.
  </tip>
</context_guidance>`

// GREP_GUIDANCE is the one-time advisory shown when Grep is used.
const GREP_GUIDANCE = `<context_guidance>
  <tip>
    This operation may flood your context window. To stay efficient:
    - Use capy_execute(language: "shell", code: "...") to run searches in the sandbox.
    - Only your final printed summary will enter the context.
  </tip>
</context_guidance>`

// BASH_GUIDANCE is the one-time advisory shown when Bash is used (and not blocked).
const BASH_GUIDANCE = `<context_guidance>
  <tip>
    This Bash command may produce large output. To stay efficient:
    - Use capy_batch_execute(commands, queries) for multiple commands
    - Use capy_execute(language: "shell", code: "...") to run in sandbox
    - Only your final printed summary will enter the context.
    - Bash is best for: git, mkdir, rm, mv, navigation, and short-output commands only.
  </tip>
</context_guidance>`
