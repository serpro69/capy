# Release Automation — Design

## Problem

capy can only be installed by cloning the repo and building from source with `CGO_ENABLED=1 -tags fts5`. This is a high barrier to adoption. Most users expect a single command (`brew install` or `curl | sh`) to get a working binary.

`go install` is not viable because `mattn/go-sqlite3` requires CGO and the `-tags fts5` build tag cannot be passed through `go install` for remote modules.

## Goals

1. Every tagged release automatically produces prebuilt binaries for 3 platforms
2. macOS users can `brew install serpro69/tap/capy`
3. All users can `curl ... | sh` to download and install the binary
4. CI catches regressions on every push to master and on PRs
5. Pre-release tags (`v1.0.0-rc.1`) produce pre-release GitHub Releases

## Non-goals

- Windows support (Unix-only syscalls: `Setpgid`, `Kill(-pid)`, `SIGHUP`)
- macOS Intel (`darwin/amd64`) — GitHub has deprecated Intel macOS runners; Apple Silicon only
- Claude Plugin marketplace distribution (separate future effort)
- Switching to pure-Go SQLite (separate decision)
- Code signing / notarization for macOS

## Development model

Trunk-based development. All work on `master`. Releases are tags in the form `vX.Y.Z` or `vX.Y.Z-rc.N`.

## Architecture

### 1. CI workflow (`.github/workflows/ci.yml`)

**Triggers:** push to `master`, PRs targeting `master`.

**Single job** on `ubuntu-latest`:
- Setup Go via `actions/setup-go@v5` with `go-version-file: go.mod`
- `go vet -tags fts5 ./...`
- `CGO_ENABLED=1 go test -tags fts5 -race ./...`

No build step in CI — that's the release workflow's job.

### 2. Release workflow (`.github/workflows/release.yml`)

**Trigger:** tag push matching `v*`.

#### Job 1: `build` (matrix)

Three entries, each on a native runner (no cross-compilation, each binary is smoke-tested on its target platform):

| GOOS | GOARCH | Runner |
|------|--------|--------|
| darwin | arm64 | `macos-latest` |
| linux | amd64 | `ubuntu-latest` |
| linux | arm64 | `ubuntu-24.04-arm` |

Concurrency: `concurrency: release-${{ github.ref }}` to prevent races if two tags are pushed in quick succession.

Each matrix entry:
1. Checkout + setup Go
2. Build: `CGO_ENABLED=1 go build -trimpath -tags fts5 -ldflags "-X github.com/serpro69/capy/internal/version.Version=${{ github.ref_name }}" -o capy ./cmd/capy/`
3. Smoke test: `./capy --version` — verify output contains the tag string (trim whitespace)
4. Package: `tar czf capy_${VERSION}_${GOOS}_${GOARCH}.tar.gz capy LICENSE.md README.md`
   - Version string strips the `v` prefix for the filename (e.g., `capy_1.0.0_darwin_arm64.tar.gz`)
5. Upload tar.gz as GitHub Actions artifact (retention 1 day)

#### Job 2: `release` (depends on build)

Runs on `ubuntu-latest`. Permissions: `contents: write`.

1. Download all build artifacts (`actions/download-artifact@v4` with `merge-multiple: true`)
2. Generate `SHA256SUMS`: `sha256sum capy_*.tar.gz > SHA256SUMS`
3. Determine pre-release: if `${{ github.ref_name }}` contains `-`, set `--prerelease` flag
4. Create GitHub Release: `gh release create $TAG --generate-notes [--prerelease] capy_*.tar.gz SHA256SUMS`

#### Job 3: `homebrew` (depends on release)

Runs on `ubuntu-latest`.

1. Checkout `serpro69/homebrew-tap` repo using `HOMEBREW_TAP_TOKEN` secret (a GitHub PAT with `repo` scope on the tap repo)
2. Download `SHA256SUMS` from the just-created release
3. Generate `Formula/capy.rb` from a template with:
   - Version extracted from tag
   - Download URLs pointing to the GitHub Release assets
   - SHA256 checksums extracted from `SHA256SUMS`
   - `depends_on` for platform requirements
4. Commit and push to the tap repo
5. Skip this job for pre-release tags (Homebrew formulas should only point to stable releases)

### 3. Install script (`install.sh`)

A POSIX-compatible shell script hosted in the repo root. Users run:

```bash
curl -sSfL https://raw.githubusercontent.com/serpro69/capy/master/install.sh | sh
```

Or with a custom install directory:

```bash
INSTALL_DIR=/usr/local/bin curl -sSfL https://raw.githubusercontent.com/serpro69/capy/master/install.sh | sh
```

Or a specific version:

```bash
VERSION=1.0.0 curl -sSfL https://raw.githubusercontent.com/serpro69/capy/master/install.sh | sh
```

The script:
1. Detects OS (`uname -s` → `darwin`/`linux`) and architecture (`uname -m` → `arm64`/`x86_64` → map to `arm64`/`amd64`)
2. Uses `VERSION` env var if set, otherwise fetches the latest release tag from the GitHub API (`/repos/serpro69/capy/releases/latest`)
3. Downloads `capy_<version>_<os>_<arch>.tar.gz` and `SHA256SUMS`
4. Verifies SHA256 checksum (`sha256sum` on Linux, `shasum -a 256` on macOS)
5. Extracts binary to `INSTALL_DIR` (default: `~/.local/bin/`, created if missing)
6. Prints success message with next steps: `Run 'capy setup' in your project directory`

Errors: fails loudly on unsupported OS/arch, download failure, checksum mismatch.

### 4. Homebrew tap repository

A separate GitHub repository: `serpro69/homebrew-tap`.

Contains `Formula/capy.rb` — a standard Homebrew formula that:
- Uses `on_macos`/`on_linux` blocks with `Hardware::CPU.arm?`/`Hardware::CPU.intel?` checks to select the correct binary URL and SHA256 for each of the 3 supported platform/arch combinations
- Downloads the prebuilt binary for the current platform from GitHub Releases
- Installs to the Homebrew prefix
- Has a `test` block that runs `capy --version`

Users install via:
```bash
brew install serpro69/tap/capy
```

The formula is auto-updated by the release workflow (Job 3 above).

### 5. README update

Add installation options to the Quick Start section, above the existing "Build from Source":

- `brew install serpro69/tap/capy` (macOS/Linux)
- `curl -sSfL .../install.sh | sh` (any Unix)
- Build from source (existing section, kept as-is)

## Secrets required

| Secret | Purpose | Where to create |
|--------|---------|-----------------|
| `HOMEBREW_TAP_TOKEN` | GitHub PAT with `repo` scope on `serpro69/homebrew-tap` | Repository settings → Secrets → Actions |

`GITHUB_TOKEN` is automatically available and sufficient for creating releases in the same repo.

## File inventory

| File | Action |
|------|--------|
| `.github/workflows/ci.yml` | Create |
| `.github/workflows/release.yml` | Create |
| `install.sh` | Create |
| `README.md` | Update Quick Start section |
