#!/usr/bin/env bash
# sandbox-files-init/start.sh
# One command to start OpenSandbox server for backend-chat.
#
# Usage (from example/backend-chat):
#   ./sandbox-files-init/start.sh
#
# Requires: Python venv with opensandbox-server installed.
# See repo root .venv or create one with: uv venv && uv pip install opensandbox-server

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG="$SCRIPT_DIR/config.toml"

# Activate venv if not already active (looks two levels up: backend-chat → example → repo root)
if [[ -z "${VIRTUAL_ENV:-}" ]]; then
  VENV="$SCRIPT_DIR/../../.venv"
  if [[ -f "$VENV/bin/activate" ]]; then
    # shellcheck disable=SC1091
    source "$VENV/bin/activate"
  else
    echo "ERROR: No virtual environment found at $VENV"
    echo "Run: uv venv && uv pip install opensandbox-server  (from repo root)"
    exit 1
  fi
fi

# Pull required Docker images if missing
for img in opensandbox/execd:v1.0.13 opensandbox/code-interpreter:latest opensandbox/egress:v1.0.8; do
  if ! docker image inspect "$img" &>/dev/null; then
    echo "Pulling $img ..."
    docker pull "$img"
  fi
done

echo "Starting OpenSandbox server on http://127.0.0.1:8080 ..."
exec opensandbox-server --config "$CONFIG"
