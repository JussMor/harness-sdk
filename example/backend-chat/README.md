# backend-chat

Independent backend project for the chat app.

It runs the harness-sdk runtime behind a small HTTP API, persists chats in
SQLite, and supports both classic request/response execution and real-time SSE
streaming.

## Run Centrifugo

```bash
docker compose up -d centrifugo
```

Centrifugo config is in `centrifugo/config.json`.

## Run Backend

```bash
cd example/backend-chat
cp .env.example .env
# export vars from .env if desired
go run .
```

By default the backend listens on `:8080`.

## API

- `GET /healthz`
- `GET /api/modes`
- `GET /api/providers`
- `GET /api/chats`
- `POST /api/chats`
- `GET /api/chats/{id}/messages`
- `POST /api/chats/{id}/messages`
- `POST /api/chats/{id}/run`
- `POST /api/chats/{id}/stream`

## Streaming API

`POST /api/chats/{id}/stream` is the real-time SSE counterpart of `/run`.

Request body:

```json
{
  "prompt": "crea dos subagents para escribir dos archivos",
  "mode": "balanced",
  "provider": "anthropic",
  "model": "claude-haiku-4-5-20251001",
  "clientRunId": "run-optional-client-id"
}
```

SSE events emitted by the backend:

- `delta` → incremental assistant text
- `tool_call` → tool invocation metadata (`name`, `args`)
- `tool_result` → tool completion payload (`name`, `content`, `error`)
- `done` → stream completed
- `error` → fatal stream failure

### Dynamic provider per request

`POST /api/chats/{id}/run` and `POST /api/chats/{id}/stream` accept optional
provider/model overrides:

- `provider`: `anthropic` | `openai` | `ollama`
- `model`: model name, with or without provider prefix

Examples:

```json
{
  "prompt": "hola",
  "provider": "anthropic",
  "model": "claude-sonnet-4-20250514"
}
```

```json
{ "prompt": "hola", "model": "openai/gpt-4o" }
```

## Runtime capabilities

The backend runtime exposes these tools to the model:

- `create-checkpoint`
- `document-operations`
- `skills-operations`
- `memory-operations`
- `dispatch-subagents`

`dispatch-subagents` fans out multiple focused subagents in parallel. Each
subagent runs with a restricted tool registry and returns a structured result
that is surfaced back through the stream as a normal tool result.

## Notes

- Messages are persisted in SQLite (`chat.db`).
- Backend publishes `message.created` events to Centrifugo channel `chat:{chatID}`.
- Runtime memory under `example/backend-chat/memory/project` and
  `example/backend-chat/memory/user` is local/generated state and is ignored by git.
