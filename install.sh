#!/bin/sh
set -eu

REPO="serpro69/capy"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

fail() { printf "Error: %s\n" "$1" >&2; exit 1; }

# Detect OS
case "$(uname -s)" in
  Darwin) OS="darwin" ;;
  Linux)  OS="linux" ;;
  *)      fail "Unsupported OS: $(uname -s). Only macOS and Linux are supported." ;;
esac

# Detect architecture
case "$(uname -m)" in
  arm64|aarch64) ARCH="arm64" ;;
  x86_64)        ARCH="amd64" ;;
  *)             fail "Unsupported architecture: $(uname -m). Only arm64 and x86_64 are supported." ;;
esac

# darwin/amd64 is not shipped
if [ "$OS" = "darwin" ] && [ "$ARCH" = "amd64" ]; then
  fail "macOS Intel (darwin/amd64) binaries are not available. Apple Silicon (arm64) only."
fi

# Resolve version
if [ -z "${VERSION:-}" ]; then
  VERSION=$(curl -sSfL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/')
  [ -n "$VERSION" ] || fail "Could not determine latest release version."
else
  # Strip v prefix if provided
  VERSION="${VERSION#v}"
fi

ARCHIVE="capy_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"

printf "Installing capy %s (%s/%s)...\n" "$VERSION" "$OS" "$ARCH"

# Download archive and checksums to a temp dir
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

curl -sSfL "${BASE_URL}/${ARCHIVE}" -o "${TMPDIR}/${ARCHIVE}" \
  || fail "Download failed: ${BASE_URL}/${ARCHIVE}"
curl -sSfL "${BASE_URL}/SHA256SUMS" -o "${TMPDIR}/SHA256SUMS" \
  || fail "Download failed: ${BASE_URL}/SHA256SUMS"

# Verify checksum
cd "$TMPDIR"
EXPECTED=$(grep "$ARCHIVE" SHA256SUMS | awk '{print $1}')
[ -n "$EXPECTED" ] || fail "Archive ${ARCHIVE} not found in SHA256SUMS."

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "$ARCHIVE" | awk '{print $1}')
else
  ACTUAL=$(shasum -a 256 "$ARCHIVE" | awk '{print $1}')
fi

[ "$EXPECTED" = "$ACTUAL" ] || fail "Checksum mismatch. Expected: ${EXPECTED}, got: ${ACTUAL}"

# Extract and install
mkdir -p "$INSTALL_DIR"
tar xzf "$ARCHIVE" capy
mv capy "$INSTALL_DIR/capy"
chmod +x "$INSTALL_DIR/capy"

printf "\nInstalled capy to %s/capy\n" "$INSTALL_DIR"

# PATH hint
case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *) printf "\nNote: %s is not in your PATH. Add it:\n  export PATH=\"%s:\$PATH\"\n" "$INSTALL_DIR" "$INSTALL_DIR" ;;
esac

printf "\nNext steps: run 'capy setup' in your project directory.\n"
