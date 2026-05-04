# OpenSandbox Setup Guide

How to run OpenSandbox server locally so **backend-chat** can create sandboxes and give the LLM bash/code/file tools.

---

## Prerequisites

- Docker Desktop (running)
- Python 3.9+ or UV
- Go 1.21+

---

## Step 1 — Install OpenSandbox server

```bash
# From repo root
uv venv                              # only once
source .venv/bin/activate
uv pip install opensandbox-server
```

---

## Step 2 — Start the sandbox server

```bash
cd example/backend-chat
./sandbox-files-init/start.sh
```

That script:
- Activates `.venv` automatically
- Pulls required Docker images if missing (`execd`, `code-interpreter`, `egress`)
- Starts the server on `http://localhost:8080` using `sandbox-files-init/config.toml`

Verify it's up:

```bash
curl http://localhost:8080/health
# → {"status":"healthy"}
```

---

## Step 3 — Configure backend-chat

```bash
cd example/backend-chat
cp .env.example .env   # sandbox vars are already included
```

The relevant lines already set in `.env.example`:

```env
OPEN_SANDBOX_DOMAIN=localhost:8080
OPEN_SANDBOX_PROTOCOL=http
OPEN_SANDBOX_API_KEY=dev-test-key
```

The API key in `.env` must match the one in `sandbox-files-init/config.toml`.

---

## Step 4 — Start backend-chat

```bash
cd example/backend-chat
go run .
# → backend-chat listening on :9090
```

---

## Port allocation

| Service          | Port  |
|------------------|-------|
| OpenSandbox      | 8080  |
| backend-chat API | 9090  |
| Centrifugo       | 8000  |
| Frontend dev     | 3000  |

---

## How sandbox tools work

When `OPEN_SANDBOX_API_KEY` is set, backend-chat automatically registers these tools for the LLM on every chat:

| Tool               | What it does                        |
|--------------------|-------------------------------------|
| `bash`             | Run shell commands inside sandbox   |
| `code_interpreter` | Execute Python/JS code              |
| `file_write`       | Write a file into the sandbox       |
| `file_read`        | Read a file from the sandbox        |

One sandbox container per chat is created lazily on first use and destroyed when the chat ends.

---

## Customising the server config

Edit `sandbox-files-init/config.toml` to change:

- **API key** — `api_key = "..."` (must match `OPEN_SANDBOX_API_KEY`)
- **Port** — `[server] port = 8080`
- **Sandbox TTL** — `max_sandbox_timeout_seconds`
- **Docker security** — `drop_capabilities`, `pids_limit`, etc.

---

## Troubleshooting

### `context deadline exceeded` on sandbox create

Docker Desktop storage state is corrupted.

```bash
pkill -f opensandbox-server
osascript -e 'quit app "Docker"'
open -a Docker
# wait until Docker is healthy, then
./sandbox-files-init/start.sh
```

### Port 8080 already in use

```bash
lsof -i :8080       # find the PID
kill <PID>
./sandbox-files-init/start.sh
```

### `opensandbox-server: command not found`

```bash
source ../../.venv/bin/activate   # from example/backend-chat
```

### Docker images missing

`start.sh` pulls them automatically. To pull manually:

```bash
docker pull opensandbox/execd:v1.0.13
docker pull opensandbox/code-interpreter:latest
docker pull opensandbox/egress:v1.0.8
```
