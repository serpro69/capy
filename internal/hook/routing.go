package hook

// RoutingBlock returns the XML routing instructions injected into subagent
// prompts and session start events. Guides the LLM on when to use capy MCP
// tools vs direct tools based on comprehension-vs-extraction needs.
func RoutingBlock() string {
	return routingBlock("    A Bash-to-general-purpose upgrade does not prove that MCP tools are available.\n", `    Claude routine capture is guidance-only. Main-agent HTTP redirects are enforced below. A child may receive one redirect to a recently
    successful suitable tool; otherwise it gets discovery/native-tool advice. Security denies
    and matched asks still apply. Bash HTTP requires observed capy_execute; WebFetch can also
    use observed capy_fetch_and_index. Git issue/PR/MR comprehension keeps native CLI guidance.
    - curl/wget in Bash → use capy_fetch_and_index or capy_execute
    - Inline HTTP in Bash → use capy_execute
    - WebFetch → for git issues/PRs/MRs: platform CLI (gh issue view) or WebSearch; for other large pages: capy_fetch_and_index`)
}

// CodexRoutingBlock shares the core guidance while describing only capy's
// selected Bash/MCP coverage. Codex child launch arguments are never rewritten.
func CodexRoutingBlock() string {
	return routingBlock("", `    Codex shell tools (including exec_command and code mode) reach hooks as Bash.
    Hosted web/search tools are not intercepted. Security and matched asks are checked first;
    matched asks deny with rule/source because this PreToolUse hook cannot request approval.
    Recognized routine tests/builds/installs require capture for roots and children before
    HTTP routing, regardless of tool evidence. Only routing.routine operator exceptions
    permit native routine execution. Nothing runs on denial; no HTTP evidence is consumed.
    Use capy_execute/capy_batch_execute or capy execute --code '<program>'. If capture
    cannot start, stop and repair it. A failed/missing tool never grants routine native fallback.
    For HTTP-only requests, silent file downloads are allowed.
    Main and child HTTP redirects each require their own capy_execute success within 60 seconds.
    Healthy running+recording or exited-zero+complete runs count; queued/failed/incomplete
    results and polling do not. Fetch alone cannot justify a Bash HTTP redirect.
    Main and child evidence are isolated; a child redirect consumes all its alternatives.
    SubagentStart supplies guidance; child launch arguments are preserved.
    Git issue/PR/MR comprehension uses native CLI.`)
}

func routingBlock(upgradeNote, blocked string) string {
	return `<context_window_protection>
  <routing_principle>
    Capture routine tests/builds/installs; Codex enforces recognized forms, Claude advises.
    - Comprehension (understand full content) → Bash, Read
    - Extraction (specific facts from large output) → capy tools
  </routing_principle>

  <direct_tools>
    Use Bash/Read for:
    - Git commands (diffs, logs, status) — always Bash
    - Small-output non-routine commands
    - Files to comprehend or edit (Read)
    - Existing logs to read; recover new captured output with capy_runs
    - Governing instructions, checklists, agent configs (Read whole)
    - Small authoritative web pages (issues, PRs, specs) — runtime web tools (gh, WebSearch) when available
  </direct_tools>

  <capy_tools>
    - capy_execute / capy_execute_file / capy_batch_execute: execute once with encrypted run
      capture and bounded results, even without intent. capy_runs: status/read/search/cancel.
      CLI alternative: capy execute; capy runs. Recover uncertain outcomes before retrying.
      New run logs are separate from knowledge; capy_search does not recover them.
    - capy_fetch_and_index: large web extraction (ephemeral; kind: "durable" for references)
    - capy_search: knowledge/session queries (batch questions as array)
  </capy_tools>

  <tool_availability>
    For HTTP-only routing, child availability is unverified until suitable execute/fetch
    success. Each expires independently after 60 seconds; one redirect consumes all alternatives.
    If the retry fails, native fallback remains available for HTTP-only requests.
    If the host offers deferred-tool discovery, discover capy schemas once.
    If discovery is unavailable or the tools are absent, use native tools only where policy
    permits. Do not loop on discovery. Routine capture requirements still apply in Codex.
` + upgradeNote + `  </tool_availability>

  <blocked>
` + blocked + `
  </blocked>

  <capy_commands>
    When the user says "capy stats" or asks about context savings:
    → Call the capy_stats MCP tool.

    When the user says "capy doctor" or asks to diagnose capy:
    → Call the capy_doctor MCP tool.
  </capy_commands>
</context_window_protection>`
}

const childToolGuidance = `capy: This child's capy tool availability is unverified.
If the host offers deferred-tool discovery, discover capy schemas once, then use suitable available tools.
If discovery is unavailable or the tools are absent, use native tools for HTTP-only requests where policy permits and keep output bounded. Routine capture requirements still apply in Codex. Do not loop on discovery.
For git issues/PRs/MRs, prefer the platform CLI or WebSearch for full comprehension.`

const codexMainToolGuidance = `capy: This main session's capy_execute availability is unverified.
If the host offers deferred-tool discovery, discover capy schemas once, then use suitable available tools.
If discovery is unavailable or the tool is absent, use native tools for HTTP-only requests where policy permits and keep output bounded. Routine capture requirements still apply in Codex. Do not loop on discovery.
For git issues/PRs/MRs, prefer the platform CLI or WebSearch for full comprehension.`

// READ_GUIDANCE is the one-time advisory shown when Read is used.
const READ_GUIDANCE = `<context_guidance>
  <tip>
    Default to reading the relevant content. Use offset/limit to scope large files.
    Use capy_execute_file for a specific question when it avoids loading substantial
    irrelevant content. If you cannot specify what to extract, read the relevant content first.
    Do not perform extraction that merely duplicates a read you still need.

    - The document is meant to be followed as instructions, applied as a checklist, or read for comprehension — use Read. Indexing turns a coherent document into a bag of fragments.
    - Finding tests related to X → search for candidates, then use Read on the relevant test blocks.
    - Extracting *derived answers* (count, stats, extracted pattern, structural summary), not the content itself; for example, counting services with a flag enabled → use capy_execute_file to extract the count.
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
    Prefer capy_execute or capy_batch_execute for routine tests/builds/installs.
    Codex requires capture for recognized routines unless explicitly excepted; Claude advises.
    Recover captured logs with capy_runs or capy runs; do not repeat execution for output.
    Use Bash for git/diffs and Read for existing logs. Read governing instructions whole.
  </tip>
</context_guidance>`
