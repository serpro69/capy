# Implement an archived session brief

## Terminal brief

Add a context-aware brief read in `internal/vault` and register the command with `newVaultCmd` in `cmd/capy/vault.go`. Use the vault key/path flow, literal prefix validation, effective metadata joins and one read transaction. Select at most two scoped metadata candidates to detect ambiguity. Use bound arguments and propagate cancellation. Exclude sidecars by the vault's main-transcript subagent ID. Query the existing `vault_fts` rows by role and numeric line/turn/message positions. Use a deterministic tie break for multiple entries on one line. Apply the cutoff before choosing a turn or a reply. An assistant-only turn preceding a user request cannot answer that request.

Work from sanitized index rows. Retrieve only selected text, limit each displayed field, neutralize terminal controls and render within one byte budget. Reserve space for identities, citations and truncation markers. Resolve a prefix without calling `GetSession`, which loads the raw blob. Avoid reading or decompressing the raw archive and sidecars. Do not restore files or open knowledge/run stores. Use the caller's deadline or a two-second query timeout, whichever comes first. Surface an expired deadline and the ordinary vault viewer command. Step → verify current and historical output on both platform fixtures, a pending reply, a duplicated opening/latest request, and unusually long multibyte text.

## MCP brief

Register a read-only tool in `internal/server/tools.go` and call the same vault operation and renderer from its handler. Reuse `vaultProjectScope` from `tool_vault_search.go`, validate the optional line strictly before lookup, and distinguish opt-in vault availability from zero matches. Scope the metadata lookup before disambiguation and before reading any text. Link to the new call only for a hit whose line belongs to the session's main transcript. Preserve the sidecar identity if search results expose a sidecar. Step → verify a default-project miss whose prefix matches another project, explicit project access, an ambiguous local prefix, and a later turn containing words that must never appear at an earlier cutoff.

## Verification

Reuse archived fixtures for new and old index versions and a remembered deletion. Test replacement between calls and consistency within one call. Inspect the plan and measure an old session in a realistically populated vault: the per-line FTS identity and ordinal columns are unindexed, so a row limit bounds results but does not bound a scan. Record vault size, cold/warm timings and timeout behavior. A routine timeout in this fixture requires revisiting the access path before release. Check registration counts/descriptions in the stdio tests, architecture and agent documentation for the actual installed tool set. [Vault schema and lookup](../../../../internal/vault/store.go) · [Session ownership](../../../adr/027-vault-is-sole-session-store.md)
