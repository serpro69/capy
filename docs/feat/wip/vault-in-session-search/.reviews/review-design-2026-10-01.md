# Design re-review: In-session vault search

Scope: design.md, implementation.md, tasks.md, and the six reconciled findings.
Method: kk:review-design, standard mode; implementation baseline f99d758.
Assessment: SOUND. No new findings after checking requirements, task ordering,
failure handling, source identities, dependency constraints, and existing TUI seams.

The corrected plan replaces the unsafe external fuzzy matcher with an explicit
bounded algorithm, moves measured feasibility into Task 1, migrates complete local
frames before hidden-target navigation, defines ANSI-independent selection, and
separates root suspension from local navigation. The prior findings are addressed
in the design; this is not evidence that their production implementations pass.

Code checks confirmed full collapsed Body text in TranscriptMessage, duplicate
SourceLine provenance, the existing root child stack, metadata-only refresh, and
the normal renderer's message-level anchors. Current app.go also routes Ctrl+G
before mode dispatch: Task 1 input ownership must precede this project editor as
well as the printable action shortcuts. Existing rename/project modals retain
priority. This follows the specified exclusive input ownership without changing
the interaction contract.

Go is the only active implementation profile (.go targets). The pinned
charmbracelet/x/ansi v0.11.6 FirstGraphemeCluster API was checked in the local
module cache and [tagged source](https://github.com/charmbracelet/x/blob/ansi/v0.11.6/ansi/parser_decode.go);
Context7 supplied only unversioned documentation. No dependency upgrade is needed.

Remaining gates are implementation evidence: source-span correctness and the
100 ms reference workload in Task 1, complete local frames in Task 2, hidden
navigation in Task 3, suspension/actions in Task 4, integrated fuzzy correctness
and latency in Task 5, and final whole-feature verification in Task 6.
