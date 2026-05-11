# Issue #43 — knowledge.db bloat fix

## Task 1: MaxSourceBytes gate in store.Index + store.IndexChunked
**Status:** done
**Dependencies:** none

- [ ] Add `DefaultMaxSourceBytes = 2 * 1024 * 1024` constant in `internal/store/`
- [ ] Add typed `SourceTooLargeError` with size and limit fields
- [ ] Add `maxSourceBytes` field to `ContentStore`, set from config in constructor
- [ ] Gate `indexPreparedChunks` — reject before chunking if `len(content) > maxSourceBytes`
- [ ] Gate `IndexChunked` — reject before entering `indexPreparedChunks` if `len(transcript) > maxSourceBytes`
- [ ] Update `NewContentStore` signature to accept `maxSourceBytes` (0 = default)
- [ ] Update all `NewContentStore` callers

## Task 2: Config field store.max_source_bytes
**Status:** done
**Dependencies:** Task 1

- [ ] Add `MaxSourceBytes int` to `StoreConfig` in `internal/config/config.go`
- [ ] Set default to `2 * 1024 * 1024` in `DefaultConfig()`
- [ ] Thread config value through to `NewContentStore` in server setup
- [ ] Validate: values <= 0 fall back to default (not disabled)

## Task 3: Lower tool_index.go file-read guard to 2 MB
**Status:** done
**Dependencies:** Task 1

- [ ] Change `maxFileSize` from `10 * 1024 * 1024` to `2 * 1024 * 1024` in `tool_index.go`
- [ ] Update error message to reference configurable `store.max_source_bytes`
- [ ] Surface `SourceTooLargeError` from `store.Index` call with clear user message

## Task 4: Threshold-aware cleanup — evict existing oversized sources
**Status:** done
**Dependencies:** Task 1, Task 2

- [ ] In `Cleanup`, after normal eviction passes, scan durable sources for `content_size > MaxSourceBytes`
- [ ] Need a way to compute source content size — add query or use `SUM(LENGTH(c.content))` join
- [ ] Flag oversized sources as evictable with reason `"oversized"`
- [ ] Respect `dryRun` — show what would be evicted
- [ ] Update cleanup output formatting to include oversized reason

## Task 5: cleanup --source \<label\> (MCP + CLI)
**Status:** done
**Dependencies:** Task 4

- [ ] Add `EvictByLabel(label string, dryRun bool)` method to `ContentStore`
- [ ] Add `source` string parameter to `capy_cleanup` MCP tool
- [ ] Add `--source` flag to `capy cleanup` CLI command
- [ ] Error clearly if label not found

## Task 6: VACUUM after cleanup when freelist > 20%
**Status:** done
**Dependencies:** Task 4

- [ ] Add `Vacuum()` method to `ContentStore` (opens dedicated connection like `Checkpoint`)
- [ ] Add `FreelistRatio()` method — returns freelist_count / page_count
- [ ] After `Cleanup` eviction (non-dry-run, non-zero evictions), check freelist ratio
- [ ] Auto-VACUUM if ratio > 0.20
- [ ] Add `--vacuum` flag to CLI cleanup for explicit trigger
- [ ] Log VACUUM duration (SQLCipher re-encrypts entire DB)

## Task 7: capy dbsize subcommand
**Status:** done
**Dependencies:** Task 1

- [ ] Add `internal/store/diskusage.go` with `DiskUsage()` method (from issue body)
- [ ] Add `cmd/capy/dbsize.go` with `newDBSizeCmd()` (from issue body)
- [ ] Wire into `root.AddCommand()` in `cmd/capy/main.go`
- [ ] Add `humanBytes` helper
