# Remember and propagate vault session deletions

Status is draft. The behavior below applies to vault owners who keep original session files on disk or exchange archives between machines. [Issue #109](https://github.com/serpro69/capy/issues/109)

## User-visible behavior

Today, `capy vault delete` removes the session row containing the raw transcript, its sidecars, search entries, and title and project overrides in one transaction. The original transcript file survives. A subsequent import can archive it again. Merge currently treats a missing UUID as a candidate to insert.

| Operation | Result for one session UUID |
| --- | --- |
| Delete with default configuration | Remove the archived session and record its UUID and effective project together in the encrypted vault. A manual import or server startup sweep skips that UUID, even if the transcript grows or moves. |
| Delete with the proposed `vault.remember_deletions = false` | Remove the local archive once. This operation produces no deletion record to share. A later disk import or merge can bring back the session. |
| Merge a deletion into a vault holding the session | Remove the destination archive and keep the deletion record so it can propagate to a third vault. Every destination honors incoming deletions, including when its own local delete setting is disabled. |
| Merge a deletion into a vault that has never held the session | Remember it there too. A future import or merge must skip it. |
| Merge a live copy into a vault that remembers the deletion | Keep the session deleted, regardless of size, timestamp, title, or project edits. Repeating either merge direction preserves the outcome. |
| Run a project-filtered merge | Select a source deletion by its effective project at deletion time, using the same literal, ASCII case-insensitive substring match as live source sessions. Apply it by exact UUID even when the destination uses a different project label. |

The local setting takes effect when issuing a delete. Changing it later preserves previously recorded deletions. Deleting a parent addresses only that UUID. Independently archived children retain their existing lifecycle. Original Claude and Codex files remain under the user's control. Deletions made before this feature have no historical record to propagate.

## Storage and merge

A deletion record, commonly called a tombstone, contains the full session UUID and the effective project captured inside the delete transaction. Prefer the custom project label when present, otherwise use the imported path. The retained record contains no transcript, session title, sidecar content, or search text. A vault owner who wants to delete once without keeping these identifiers can opt out before deletion.

Store unique `(session_uuid, effective_project)` pairs independently of live session rows. Any pair suppresses the entire UUID locally. If two machines independently delete the same UUID under different project labels, merge retains both observed pairs. This keeps both project filters usable on subsequent merges. A filtered merge carries only the matching pairs. Full merges combine all pairs. UUID membership and pair union make repeated merges idempotent without clocks or machine-ID tie-breaks.

Commit removal of the live rows and insertion of the marker together. The destination write transaction must check for a marker before creating or replacing session data, including batched imports and individual retries. This closes the race where an import examined a live transcript before another process deleted it. Report a suppressed session as skipped or excluded with a deletion reason. A marker received through merge uses the same transactional removal path and bypasses admission filters intended for live transcripts.

Keep merge dry-run useful. Show the UUIDs whose archives would be removed and distinguish those from markers that would only be recorded or that are already present. Actual merge reports distinguish the same outcomes. Update CLI help and the vault reference because a merge carrying a deletion can now remove destination data.

## Compatibility and scope

Use an additive vault migration. Raise the vault's minimum reader version in the transaction that first stores a deletion marker. A capable binary can merge from a legacy vault containing live sessions and no markers. The absence of a legacy row conveys no deletion. Upgrade and restart all writers attached to a shared vault before enabling the new behavior; the existing version check runs at open and cannot protect an already-open old connection. Document the existing limitation for binaries predating version checks.

The design assumes session UUIDs retain their existing vault-wide identity. Markers persist because an offline vault or original transcript may reappear at any time. Bulk deletion belongs to #136. Physical erasure of SQLite pages, backups, or original transcripts and a synchronized undelete operation are outside #109.

## Alternatives considered

Local-only suppression lets a merge peer retain and later forward its copy. The requested propagation requires that peer to remember the deletion. Applying the opt-out when importing would let a destination ignore an incoming deletion. The setting therefore governs local deletion only. A UUID-only record would lose the information needed by the existing source-project filter once the archive is gone.
