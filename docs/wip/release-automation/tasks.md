# Tasks: Release Automation

> Design: [./design.md](./design.md)
> Status: pending
> Created: 2026-03-30

## Task 1: CI workflow

- **Status:** done
- **Depends on:** —
- **Docs:** [design.md#1-ci-workflow](./design.md#1-ci-workflow-githubworkflowsci-yml)

### Subtasks
- [x] 1.1 Create `.github/workflows/ci.yml` — trigger on push to `master` and PRs targeting `master`
- [x] 1.2 Single job on `ubuntu-latest`: setup Go via `go-version-file: go.mod`, run `go vet -tags fts5 ./...`, then `CGO_ENABLED=1 go test -tags fts5 -race ./...`
- [ ] 1.3 Verify: push a branch, open a PR to master, confirm the workflow runs and passes

## Task 2: Release workflow — build matrix

- **Status:** done
- **Depends on:** —
- **Docs:** [design.md#2-release-workflow](./design.md#2-release-workflow-githubworkflowsrelease-yml)

### Subtasks
- [x] 2.1 Create `.github/workflows/release.yml` — trigger on tag push `v*`
- [x] 2.2 Define `build` job with matrix strategy: 3 entries (`darwin/arm64` on `macos-latest`, `linux/amd64` on `ubuntu-latest`, `linux/arm64` on `ubuntu-24.04-arm`). Add `concurrency: release-${{ github.ref }}`
- [x] 2.3 Each matrix entry: checkout, setup Go, `CGO_ENABLED=1 go build -trimpath -tags fts5 -ldflags "-X .../version.Version=$TAG" -o capy ./cmd/capy/`
- [x] 2.4 Smoke test: `./capy --version` and assert output contains the tag string (trim whitespace)
- [x] 2.5 Package: `tar czf capy_<version>_<os>_<arch>.tar.gz capy LICENSE.md README.md` (strip `v` prefix from version in filename)
- [x] 2.6 Upload tar.gz as GitHub Actions artifact with `retention-days: 1`

## Task 3: Release workflow — release job

- **Status:** done
- **Depends on:** Task 2
- **Docs:** [design.md#2-release-workflow](./design.md#2-release-workflow-githubworkflowsrelease-yml)

### Subtasks
- [x] 3.1 Add `release` job depending on `build`, running on `ubuntu-latest` with `permissions: contents: write`
- [x] 3.2 Download all build artifacts via `actions/download-artifact@v4` with `merge-multiple: true`
- [x] 3.3 Generate `SHA256SUMS` file: `sha256sum capy_*.tar.gz > SHA256SUMS`
- [x] 3.4 Detect pre-release: if tag (`github.ref_name`) contains `-`, pass `--prerelease` to `gh release create`
- [x] 3.5 Create GitHub Release: `gh release create $TAG --generate-notes [--prerelease] capy_*.tar.gz SHA256SUMS`

## Task 4: Install script

- **Status:** done
- **Depends on:** Task 3
- **Docs:** [design.md#3-install-script](./design.md#3-install-script-install-sh)

### Subtasks
- [x] 4.1 Create `install.sh` in repo root — POSIX-compatible shell script
- [x] 4.2 Detect OS (`uname -s` → `darwin`/`linux`) and architecture (`uname -m` → map `x86_64`→`amd64`, `aarch64`/`arm64`→`arm64`)
- [x] 4.3 Fetch latest release tag from GitHub API (`/repos/serpro69/capy/releases/latest`), or accept `VERSION` env var override
- [x] 4.4 Download `capy_<version>_<os>_<arch>.tar.gz` and `SHA256SUMS` from the release
- [x] 4.5 Verify SHA256 checksum (`sha256sum` on Linux, `shasum -a 256` on macOS), fail on mismatch
- [x] 4.6 Extract binary to `INSTALL_DIR` (default `~/.local/bin/`, create if missing)
- [x] 4.7 Print success message with next steps (`capy setup` in project directory, PATH hint if `~/.local/bin` is not in PATH)
- [x] 4.8 Handle errors: unsupported OS/arch, download failure, checksum mismatch — fail loudly with actionable message

## Task 5: Homebrew tap

- **Status:** done
- **Depends on:** Task 3
- **Docs:** [design.md#4-homebrew-tap-repository](./design.md#4-homebrew-tap-repository)

### Subtasks
- [x] 5.1 Add `homebrew` job to `release.yml` depending on `release`, skipped for pre-release tags
- [x] 5.2 Checkout `serpro69/homebrew-tap` using `HOMEBREW_TAP_TOKEN` secret
- [x] 5.3 Download `SHA256SUMS` from the release
- [x] 5.4 Generate `Formula/capy.rb` — standard Homebrew formula with: version, `on_macos`/`on_linux` blocks with `Hardware::CPU.arm?`/`Hardware::CPU.intel?` for 3 platform/arch URL+SHA256 combos, `test` block running `capy --version`
- [x] 5.5 Commit and push the updated formula to the tap repo

## Task 6: README update

- **Status:** pending
- **Depends on:** Task 4, Task 5
- **Docs:** [design.md#5-readme-update](./design.md#5-readme-update)

### Subtasks
- [ ] 6.1 Add installation options to README Quick Start section, above "Build from Source": `brew install serpro69/tap/capy`, `curl -sSfL .../install.sh | sh`, and direct download from GitHub Releases
- [ ] 6.2 Keep existing "Build from Source" section as-is

## Task 7: Final verification

- **Status:** pending
- **Depends on:** Task 1–6

### Subtasks
- [ ] 7.1 Run `testing-process` skill — verify all existing tests still pass
- [ ] 7.2 Run `documentation-process` skill — update CONTRIBUTING.md if needed (release process docs)
- [ ] 7.3 Run `implementation-review` skill — verify implementation matches design doc
