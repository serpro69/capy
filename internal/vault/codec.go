package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/klauspost/compress/zstd"
)

// Blob encoding discriminators stored in the `encoding` column beside each blob
// (vault_sessions.encoding for raw_jsonl, vault_files.encoding for raw_content).
// The column is authoritative — NOT magic-byte detection. raw_jsonl is always
// JSONL, but vault_files.raw_content holds ARBITRARY sidecar bytes (screenshots,
// build logs, already-compressed files; see discovery.go collectAssociatedFiles),
// so peeking a magic header would mis-decode a raw sidecar that happens to begin
// with the zstd magic, or a sidecar that is itself a zstd file. Legacy v1 rows
// predate the column and read as NULL, treated as raw. See design.md §1.
const (
	encodingRaw  = "raw"
	encodingZstd = "zstd"
)

// noCompressEnv, when set to any non-empty value, forces encodeBlob to store raw
// bytes. It is the staggered-rollout escape hatch (design.md §1 Downgrade safety):
// a vault written entirely under it never gains a compressed blob nor the
// min_reader_version marker, so it stays readable by an older v1 binary.
const noCompressEnv = "CAPY_VAULT_NO_COMPRESS"

// Reader-version milestones. Every on-disk capability an older binary cannot read
// safely has a named version here, and the write that introduces such a record
// stamps vault_meta.min_reader_version with it.
//
// RULE (design § Reader version, ADR-031): adding a Platform constant
// (platform.go, knownPlatforms) is a reader-version bump. Every read path that
// dispatches on platform is exhaustive over the constants it knows, so a binary
// lacking a constant must refuse the vault at open rather than mis-read its rows
// (an older binary's restore/resume/merge are platform-blind and would corrupt).
// TestPlatform_ReaderVersionPairing pins each constant to its version.
//
// Every stamping site passes the version ITS OWN write requires, never
// supportedReaderVersion: the batch writer stamps the highest version any record
// in the transaction needs (store.go requiredReaderVersion), and `capy vault
// compact` (compact.go markCompressed), which only recompresses blobs, stamps
// readerVersionZstd — so compacting a Claude-only vault leaves the marker at 2
// and older binaries can still open it.
const (
	// readerVersionZstd: zstd-encoded blobs (vault v2, `encoding` column).
	readerVersionZstd = 2
	// readerVersionPlatform: rows whose platform is not claude-code (Codex
	// sessions, migration 0006). Every older binary is platform-blind — its
	// restore would write a Codex row under ~/.claude/projects, its resume would
	// launch `claude --resume` on a Codex uuid, and its merge would rescan the
	// blob with the Claude scanner and store it as claude-code with empty FTS, a
	// permanent mislabel — so the first such row must lock older binaries out.
	readerVersionPlatform = 3
)

// supportedReaderVersion is the highest vault min_reader_version this binary can
// read: readerVersionPlatform, since it knows every platform constant. A vault
// whose marker exceeds this (a future on-disk format this binary predates) is
// refused on open by checkReaderVersion rather than silently mis-read.
const supportedReaderVersion = readerVersionPlatform

// minReaderVersionKey is the vault_meta row key carrying the minimum reader
// version required to safely read the vault. Absent ⇒ no constraint (a v1 vault,
// or one never written with compression).
const minReaderVersionKey = "min_reader_version"

// blobEncoder/blobDecoder are shared package-level instances. EncodeAll/DecodeAll
// on a single Encoder/Decoder are thread-safe and reentrant (verified via
// context7), so this one pair serves the server sweep goroutine and the CLI with
// no locking and no per-call compressor allocation. A nil writer/reader selects
// the stateless EncodeAll/DecodeAll mode (no streaming pipeline). The decoder is
// never Closed: it lives for the process lifetime.
var (
	blobEncoder *zstd.Encoder
	blobDecoder *zstd.Decoder
)

func init() {
	var err error
	if blobEncoder, err = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression)); err != nil {
		// Fixed options, no I/O — a creation error here is a programmer error.
		panic(fmt.Sprintf("vault: creating zstd encoder: %v", err))
	}
	if blobDecoder, err = zstd.NewReader(nil); err != nil {
		panic(fmt.Sprintf("vault: creating zstd decoder: %v", err))
	}
}

// encodeBlob compresses b for storage, returning the bytes to store and the
// encoding discriminator to persist alongside them. It returns ("raw") when
// CAPY_VAULT_NO_COMPRESS is set or when compression does not shrink the input
// (tiny or incompressible blobs), so the stored bytes are never larger than the
// input and a "raw" label always round-trips verbatim. content_hash, size_bytes,
// and FTS text are computed on the UNCOMPRESSED bytes upstream (import.go), so
// compression is purely a storage encoding applied at this write seam.
func encodeBlob(b []byte) (data []byte, encoding string) {
	if os.Getenv(noCompressEnv) != "" {
		return b, encodingRaw
	}
	compressed := blobEncoder.EncodeAll(b, nil)
	if len(compressed) >= len(b) {
		// No win (incompressible / tiny). Store raw so reads skip decompression
		// and the blob never grows.
		return b, encodingRaw
	}
	return compressed, encodingZstd
}

// decodeBlob reverses encodeBlob using the persisted encoding discriminator. A
// NULL/"" or "raw" encoding (legacy v1 rows, or rows where compression was
// declined) returns the bytes unchanged; "zstd" decompresses. An unrecognized
// encoding is a hard error, never a silent passthrough — fail loud on a blob this
// binary cannot read rather than handing back compressed bytes as if they were raw.
func decodeBlob(encoding string, b []byte) ([]byte, error) {
	switch encoding {
	case "", encodingRaw:
		return b, nil
	case encodingZstd:
		out, err := blobDecoder.DecodeAll(b, nil)
		if err != nil {
			return nil, fmt.Errorf("decompressing zstd blob: %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown blob encoding %q", encoding)
	}
}

// markMinReaderVersion records that the vault now holds a record requiring a
// reader that supports version, within the caller's write transaction. It is a
// MONOTONIC upsert: it creates an absent marker and raises a lower one, but
// never lowers an existing marker — a later zstd-only write on a vault already
// holding a Codex row must not roll 3 back to 2. Callers pass the version their
// own write requires (readerVersionZstd / readerVersionPlatform), never
// supportedReaderVersion (see the readerVersion* comment). It cannot protect a
// v1 binary (which predates the marker and reads compressed bytes as garbage — a
// documented cross-version constraint), but every binary since refuses a vault
// whose marker it does not support (checkReaderVersion) instead of mis-reading.
//
// The marker is stored as TEXT (vault_meta.value); the comparison casts both
// sides to INTEGER so "10" never sorts below "9".
func markMinReaderVersion(ctx context.Context, tx *sql.Tx, version int) error {
	if version <= 0 {
		return fmt.Errorf("marking min_reader_version: invalid version %d", version)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO vault_meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value
		 WHERE CAST(vault_meta.value AS INTEGER) < CAST(excluded.value AS INTEGER)`,
		minReaderVersionKey, strconv.Itoa(version)); err != nil {
		return fmt.Errorf("marking min_reader_version: %w", err)
	}
	return nil
}

// checkReaderVersion refuses to open a vault whose min_reader_version exceeds this
// binary's supportedReaderVersion — a newer on-disk format this binary predates.
// An absent marker (a v1 vault, or one never written with compression) imposes no
// constraint. Called on open after the schema and migrations are in place, so
// vault_meta is guaranteed to exist; without this read the marker is inert and
// protects no one (design.md §1).
func checkReaderVersion(ctx context.Context, db *sql.DB) error {
	var value string
	err := db.QueryRowContext(ctx,
		`SELECT value FROM vault_meta WHERE key = ?`, minReaderVersionKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // no marker — unconstrained (v1 / never-compressed vault)
	}
	if err != nil {
		return fmt.Errorf("reading min_reader_version: %w", err)
	}
	required, perr := strconv.Atoi(value)
	if perr != nil {
		// A non-numeric marker is corruption we must surface, not paper over.
		return fmt.Errorf("invalid min_reader_version %q in vault: %w", value, perr)
	}
	if required > supportedReaderVersion {
		return fmt.Errorf("vault requires reader version %d but this capy binary supports %d; upgrade capy to open it",
			required, supportedReaderVersion)
	}
	return nil
}
