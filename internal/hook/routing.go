package hook

// RoutingBlock returns the XML routing instructions injected into subagent
// prompts and session start events. Guides the LLM on when to use capy MCP
// tools vs direct tools based on comprehension-vs-extraction needs.
func RoutingBlock() string {
	return `<context_window_protection>
  <routing_principle>
    Choose tools based on what you need from the output:
    - Comprehension (understand full content) → Bash, Read
    - Extraction (specific facts from large output) → capy tools
  </routing_principle>

  <direct_tools>
    Use Bash/Read for:
    - Git commands (diffs, logs, status) — always Bash
    - Small-output commands (less than ~50 lines)
    - Files to comprehend or edit (Read)
    - Sequential/ordered content (test output, build logs)
    - Instruction files, checklists, configs (Read whole)
    - Small authoritative web pages (issues, PRs, specs) — runtime web tools (gh, WebSearch) when available
  </direct_tools>

  <capy_tools>
    Use capy for:
    - capy_batch_execute: broad exploration, multiple commands + queries in ONE call
    - capy_execute / capy_execute_file: large-output extraction (hundreds+ lines)
    - capy_fetch_and_index: large web content for extraction (default ephemeral; kind: "durable" for reference docs). NOT for small pages needing comprehension — use runtime web tools
    - capy_search: query indexed content (batch questions as array)
  </capy_tools>

  <tool_availability>
    A child agent's capy tool availability is unverified; agent type alone is not proof.
    A successful capy_execute or capy_fetch_and_index call can justify one suitable redirect
    for that child within 60 seconds. Each tool expires independently; a redirect consumes
    all alternatives for that child. If the retry fails, native fallback remains available.
    If the host offers deferred-tool discovery, discover capy schemas once, then use available tools.
    If discovery is unavailable or the tools are absent, use native tools following the
    comprehension/extraction principle and keep extracted output bounded. Do not loop on discovery.
    A Bash-to-general-purpose upgrade does not prove that MCP tools are available.
  </tool_availability>

  <blocked>
    Main-agent redirects are enforced below. A child may receive one redirect to a recently
    successful suitable tool; otherwise it gets discovery/native-tool advice. Security denies
    and matched asks still apply. Bash HTTP requires observed capy_execute; WebFetch can also
    use observed capy_fetch_and_index. Git issue/PR/MR comprehension keeps native CLI guidance.
    - curl/wget in Bash → use capy_fetch_and_index or capy_execute
    - Inline HTTP in Bash → use capy_execute
    - WebFetch → for git issues/PRs/MRs: platform CLI (gh issue view) or WebSearch; for large content: capy_fetch_and_index
  </blocked>

  <output_constraints>
    <word_limit>Keep your final response under 500 words.</word_limit>
    <artifact_policy>
      Write artifacts (code, configs, PRDs) to FILES. NEVER return them as inline text.
      Return only: file path + 1-line description.
    </artifact_policy>
  </output_constraints>

  <capy_commands>
    When the user says "capy stats" or asks about context savings:
    → Call the capy_stats MCP tool and display the full output verbatim.

    When the user says "capy doctor" or asks to diagnose capy:
    → Call the capy_doctor MCP tool and display results as a checklist.
  </capy_commands>
</context_window_protection>`
}

const childToolGuidance = `capy: This child's capy tool availability is unverified.
If the host offers deferred-tool discovery, discover capy schemas once, then use suitable available tools.
If discovery is unavailable or the tools are absent, use native tools following the comprehension/extraction principle and keep extracted output bounded. Do not loop on discovery.
For git issues/PRs/MRs, prefer the platform CLI or WebSearch for full comprehension.`

// READ_GUIDANCE is the one-time advisory shown when Read is used.
const READ_GUIDANCE = `<context_guidance>
  <tip>
    Read is the right default. Use offset/limit to scope large files.
    Only reach for capy_execute_file when the file is genuinely large (10k+ lines)
    AND you want a derived answer (count, stats, extracted pattern), not the content itself.
    If an Edit will follow, just Read — capy_execute_file beforehand is pure overhead.
  </tip>
</context_guidance>`

// GREP_GUIDANCE is the one-time advisory shown when Grep is used.
const GREP_GUIDANCE = `<context_guidance>
  <tip>
    Grep results can be large. If you need all matches for comprehension, this is fine.
    For extraction from large result sets, use capy_execute(language: "shell", code: "grep ...")
    to run in sandbox — only your printed summary enters context.
  </tip>
</context_guidance>`

// BASH_GUIDANCE is the one-time advisory shown when Bash is used (and not blocked).
const BASH_GUIDANCE = `<context_guidance>
  <tip>
    This Bash command may produce large output. Consider:
    - If you need to comprehend the full output (diffs, test results, logs): Bash is correct.
    - If you only need extracted facts from large output: use capy_execute or capy_batch_execute.
    - For multiple commands + search in ONE call: capy_batch_execute.
  </tip>
</context_guidance>`
