#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo 'usage: capture-smoke.sh /path/to/capy' >&2
  exit 2
fi
binary="$(cd "$(dirname "$1")" && pwd -P)/$(basename "$1")"
test -x "$binary"
smoke_root=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/capy-capture-smoke.XXXXXX")
trap 'rm -rf "$smoke_root"' EXIT
mkdir -p "$smoke_root/project" "$smoke_root/home" "$smoke_root/config" "$smoke_root/data"

# Every invocation is a new process with isolated credentials/configuration.
# Recovery must read the built artifact's retained history without rerunning it.
run_capy() {
  env -i PATH="$PATH" HOME="$smoke_root/home" \
    XDG_CONFIG_HOME="$smoke_root/config" XDG_DATA_HOME="$smoke_root/data" \
    XDG_CACHE_HOME="$smoke_root/cache" CLAUDE_CONFIG_DIR="$smoke_root/claude" \
    CODEX_HOME="$smoke_root/codex" CAPY_VAULT_PATH="$smoke_root/vault.db" \
    CAPY_DB_KEY=synthetic-release-capture-key CAPY_VAULT_KEY=synthetic-release-vault-key \
    "$binary" --project-dir "$smoke_root/project" "$@"
}

exit_status=0
run_capy execute --language shell --timeout-ms 30000 \
  --code 'printf x >> invocation-count; printf "capture-smoke-stdout\n"; printf "capture-smoke-stderr\n" >&2; exit 7' \
  --json > "$smoke_root/execute.json" || exit_status=$?
test "$exit_status" -eq 7
run_id=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["run_id"])' "$smoke_root/execute.json")
test -n "$run_id"
run_capy runs status --run-id "$run_id" --json > "$smoke_root/status.json"
run_capy runs read --run-id "$run_id" --stream stdout --raw --encoding base64 --json > "$smoke_root/stdout.json"
run_capy runs read --run-id "$run_id" --stream stderr --raw --encoding base64 --json > "$smoke_root/stderr.json"

python3 - "$smoke_root" <<'PY'
import base64
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
initial = json.loads((root / "execute.json").read_text())
status = json.loads((root / "status.json").read_text())["run"]
scope, state = {
    "darwin": ("darwin_process_group_observation_v1", "observed"),
    "linux": ("linux_descendants_v1", "retired"),
}[sys.platform]
for result in (initial, status):
    assert result["version"] == 2, result
    assert result["state"] == "exited" and result["exit_code"] == 7, result
    assert result["capture"]["state"] == "complete", result
    assert result["capture"]["terminal_persisted"] is True, result
    assert result["cleanup"] == {"scope": scope, "state": state}, result
    assert result["run_id"] == initial["run_id"], result
    assert result["owner_id"] == initial["owner_id"], result
for stream in ("stdout", "stderr"):
    page = json.loads((root / (stream + ".json")).read_text())
    assert page["eof"] is True and page["next_cursor"] is None, page
    assert page["run_id"] == initial["run_id"], page
    data = b"".join(base64.b64decode(part["data"], validate=True) for part in page["fragments"])
    assert data == ("capture-smoke-" + stream + "\n").encode(), (stream, data)
assert (root / "project/invocation-count").read_bytes() == b"x"
database = root / "project/.capy/runs/runs.db"
assert database.is_file()
with database.open("rb") as handle:
    assert handle.read(16) != b"SQLite format 3\x00"
print("Release capture/recovery passed: once-only stdout/stderr, exit 7, encrypted history, " + scope + "/" + state)
PY
