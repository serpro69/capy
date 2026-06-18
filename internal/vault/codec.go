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

// supportedReaderVersion is the highest vault min_reader_version this binary can
// read. v2 introduces zstd blobs, so a compressed vault is stamped
// min_reader_version="2" (markMinReaderVersion) and this binary supports 2. A
// vault whose marker exceeds this (a future on-disk format this binary predates)
// is refused on open by checkReaderVersion rather than silently mis-read.
const supportedReaderVersion = 2

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

// markMinReaderVersion records, idempotently, that the vault now holds compressed
// blobs and therefore requires a reader supporting supportedReaderVersion. It is
// called within the write transaction whenever encodeBlob produced a zstd blob.
// INSERT OR IGNORE keeps it a cheap index probe once the marker exists. It cannot
// protect a v1 binary (which predates the marker and reads compressed bytes as
// garbage — a documented cross-version constraint), but a future v3 that bumps the
// marker is then refused by a v2 binary's checkReaderVersion instead of mis-reading.
func markMinReaderVersion(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO vault_meta (key, value) VALUES (?, ?)`,
		minReaderVersionKey, strconv.Itoa(supportedReaderVersion)); err != nil {
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
