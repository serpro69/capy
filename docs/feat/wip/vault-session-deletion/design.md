# Remember and propagate vault session deletions

Revised after two design reviews. Implementation is pending. The behavior below applies to vault owners who keep original session files on disk or exchange archives between machines. [Issue #109](https://github.com/serpro69/capy/issues/109)

## Requirements and identity

Issue #109 requests archive cleanup and suppression of disk re-import, with a configuration opt-out. During design on 2026-11-21 the maintainer also explicitly chose outbound deletion propagation, unconditional acceptance of incoming deletions, and source effective-project filtering. The configuration choice applies when issuing a local delete. [Review decisions](./review-resolution.md)

A session keeps its UUID when copied to another vault. Suppose two machines have checkouts at `/home/alex/capy` and `/src/capy`. A session started on A and a separate session started on B have different UUIDs. If B has previously merged A's session, deleting it on A and merging A into B removes that copy from B. B keeps its own session. A full merge carries every recorded pair. With `--project capy`, selection uses the source's effective project, which can be a label or a path containing `capy`. In both cases the destination matches the archive by exact UUID.

## User-visible behavior

Today, `capy vault delete` removes the session row containing the raw transcript, its sidecars, search entries, and title and project overrides in one transaction. The original transcript file survives. A subsequent import can archive it again. Merge currently treats a missing UUID as a candidate to insert.

| Operation | Result for one session UUID |
| --- | --- |
| Delete with default configuration | Remove the archived session and record its UUID and effective project together in the encrypted vault. A manual import or server startup sweep excludes that UUID, even if the transcript grows or moves. |
| Delete with the proposed `vault.remember_deletions = false` | Remove the local archive once. This operation produces no deletion record to share. A later disk import or merge can bring back the session. |
| Merge a deletion into a vault holding the session | Remove the destination archive by UUID and retain exactly the selected source pairs for forwarding, alongside any pairs already recorded. Every destination honors incoming deletions, including when its own local delete setting is disabled. |
| Merge a deletion into a vault that has never held the session | Remember it there too. A future import or merge excludes a live copy of that UUID. |
| Merge a live copy into a vault that remembers the deletion | Keep the session deleted, regardless of size, timestamp, title, or project edits. Repeating either merge direction preserves the outcome. |
| Run a project-filtered merge | Select a source deletion by its effective project at deletion time, using the same literal, ASCII case-insensitive substring match as live source sessions. Apply it by exact UUID even when the destination uses a different project label. |

The preview for a remembered delete identifies the full UUID and tells the operator that capy cannot undo this deletion, imports will exclude this UUID, and a merge can delete copies in other vaults. For a one-time delete it explains that the archive can return. Show this information before confirmation, including in the preview printed with `--yes`. Changing the setting later preserves existing deletion records. Independently archived children keep their existing lifecycle. Original Claude and Codex files remain under the user's control. Deletions made before this feature have no historical record to propagate.

## Stored deletion records

A deletion record, commonly called a tombstone, contains the full session UUID and the effective project captured inside the delete transaction. Prefer the custom project label when present, otherwise use the imported path. The retained record contains no transcript, session title, sidecar content, or search text. A vault owner who wants to delete once without keeping these identifiers can opt out before deletion.

Store unique `(session_uuid, effective_project)` pairs independently of live session rows. Any pair suppresses the UUID locally. Only an explicit remembered delete captures the local effective project. Two machines that independently delete the same UUID under different labels can contribute two pairs. Receiving a deletion imports the selected pairs verbatim. For example, if A issued the delete under `personal` and B receives it while holding the UUID under `work`, a later full merge or `--project personal` from B forwards it. A `--project work` merge requires an independently recorded matching pair.

Commit each UUID's archive removal and new pairs together. On the first remembered deletion for a UUID, clean that UUID from all three search indexes even if its archive is already absent. An older writer could leave search rows after a concurrent deletion. A live archive also requires cleanup. Once a UUID has a marker and no live row, a repeat or an additional project pair issues zero search-index deletes for it. Batch this work by distinct UUID and leave other sessions' indexes alone.

## Import and concurrent writes

For each selected disk-import candidate, check the UUID before the importer reads the transcript or sidecars, in preview and apply. Recheck inside the transaction that writes archive data, search entries, a location hint, or merge metadata. A remembered UUID yields `StatusExcluded` with reason `deleted`. If a planned replacement, rebuild, or location update encounters an absent parent with no marker, return `StatusSkipped` with reason `session disappeared` and perform no child writes. An ordinary later import can admit it as a new session. Report the committed outcome for each input, including each batch retry and each startup-sweep candidate.

## Merge and reporting

Merge reads the source's markers and live records from one SQLite snapshot. With separate reads, a source deletion can commit between reading the marker set and enumerating live UUIDs. Both reads can omit the UUID even though every committed source state contains either the archive or its marker. The next merge can pick up a deletion that committed after the selected snapshot.

| Destination before merge | Live archives removed | Selected pairs added | Selected pairs already present |
| --- | ---: | ---: | ---: |
| One live UUID receives two new project pairs | 1 | 2 | 0 |
| The same merge runs again | 0 | 0 | 2 |
| An empty vault receives both pairs | 0 | 2 | 0 |

Print the full UUID for deletion outcomes. Preview reports what would change in the observed destination. It leaves archive contents, search rows, deletion records, and the reader requirement untouched. The normal vault open and source WAL checkpoint still apply. An apply reports committed batches. If a later batch fails or the running command observes cancellation before completion, show the partial results and return a failure. Retrying recognizes previously committed pairs.

## Compatibility

Migration `0008_session_deletions` creates `vault_session_deletions` with `session_uuid TEXT NOT NULL`, `effective_project TEXT NOT NULL` and primary key `(session_uuid, effective_project)`. The record outlives the session and has no foreign key to it. Empty imported project paths retain their existing representation. Table creation alone preserves the old reader requirement. The first locally recorded or received pair stamps `readerVersionSessionDeletions = 4` in the same transaction. Platform writes still require at most 3 and compression alone at most 2. Later writes preserve the highest requirement. Legacy sources contribute live sessions and zero markers.

Upgrade and restart all writers attached to a shared vault before enabling the behavior. The open-time version check protects subsequent opens by marker-aware older binaries. A writer already attached or a binary predating version checks does not have that protection. Ship local recording, suppression, merge propagation and its CLI notices in the same release. An intermediate build that merely recognizes the new table cannot safely advertise reader version 4.

## Assumptions

The existing vault-wide UUID identifies the same archive across copies, including when paths or project labels differ. An offline vault or an original transcript may reappear at any time, so deletion records persist.

## Not Doing

- Local or synchronized undelete. A peer can forward a retained deletion again. Reversing that decision across replicas needs its own contract.
- Bulk deletion. Issue #136 tracks that workflow.
- Physical erasure of SQLite pages, backups, or original transcripts. This feature governs the live vault and its import and merge behavior.

## Alternatives considered

Local-only suppression lets a merge peer retain and later forward its copy. The requested propagation requires that peer to remember the deletion. Applying the opt-out when importing would let a destination ignore an incoming deletion. The setting therefore governs local deletion only. A UUID-only record would lose the information needed by the existing source-project filter once the archive is gone.
