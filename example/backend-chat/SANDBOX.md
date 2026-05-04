# Sandbox — Documentación del sistema

Este documento describe el sistema de sandbox que alimenta la ejecución de código
y el renderizado de artifacts en el chat.

---

## Arquitectura

```
Usuario escribe prompt
        ↓
Backend (Runtime.Run / RunStream)
        ↓
LLM decide usar bash / code_interpreter / file_write / file_read
        ↓
SafetyFilter inspecciona el tool call (bloquea comandos peligrosos)
        ↓
OpenSandbox CodeInterpreter ejecuta código
        ↓
Backend retorna resultado al LLM
        ↓
Stream emite eventos al frontend:
  event: tool_call      → el LLM invocó una tool
  event: tool_result    → la tool retornó texto plano
  event: sandbox_output → la tool retornó output rico (HTML, imagen, SVG)
        ↓
Frontend renderiza en canvas lateral (iframe sandbox)
```

---

## Componentes

### `sdk/providers/sandbox/opensandbox.go`

Implementa `autobuild.SandboxDriver` usando el SDK de OpenSandbox.

| Método | OpenSandbox API | Descripción |
|--------|-----------------|-------------|
| `Create()` | `CreateCodeInterpreter()` | Crea un nuevo sandbox con código-intérprete |
| `Exec()` | `Sandbox.RunCommand()` | Ejecuta bash commands |
| `ExecCode()` | `CodeInterpreter.Execute()` | Ejecuta código con resultados MIME |
| `ExecCodeStreaming()` | `Execute() + ExecutionHandlers` | Ejecuta con callbacks de output en vivo |
| `WriteFile()` | `Sandbox.UploadFile()` | Sube archivo al sandbox |
| `ReadFile()` | `Sandbox.DownloadFile()` | Descarga archivo del sandbox |
| `Destroy()` | `Sandbox.Kill()` | Termina el sandbox |
| `Status()` | `Sandbox.GetInfo()` | Estado del sandbox |
| `IP()` | `Sandbox.GetEndpoint(8080)` | URL pública del sandbox |

### `backend-chat/sandbox_provider.go`

Herramientas expuestas al LLM:

| Tool | Cuándo usar |
|------|-------------|
| `bash` | Comandos de shell: instalar deps, manipular archivos, scripts |
| `code_interpreter` | Código Python/JS/Bash con output rico (charts, tablas, imágenes) |
| `file_write` | Guardar archivos en el sandbox (scripts, datos, HTML) |
| `file_read` | Leer archivos del sandbox |

### `backend-chat/main.go` — SSE events

```
event: sandbox_output
data: {
  "has_rich_output": true,
  "text": "stdout + annotations de rich output"
}
```

---

## Configuración

Variables de entorno requeridas:

```bash
OPEN_SANDBOX_API_KEY=...       # API key de OpenSandbox
OPEN_SANDBOX_DOMAIN=...        # e.g. api.open-sandbox.ai (sin protocol)
OPEN_SANDBOX_PROTOCOL=https    # https (default) o http para local
```

Cuando `OPEN_SANDBOX_API_KEY` no está configurado:
- Las tools `bash`, `code_interpreter`, `file_write`, `file_read` **no se registran**
- El LLM no las verá en el registry
- El backend funciona normalmente con las otras tools

---

## Lifecycle del sandbox

```
Primer uso del chat (bash/code_interpreter/file_write/file_read)
  → sandboxManager.getOrCreateSandbox(chatID)
  → OpenSandbox crea CodeInterpreter con TTL=900s (15 min)
  → sandboxID guardado en cache por chatID

Turnos siguientes del mismo chat
  → cache hit → mismo sandbox
  → estado persiste (variables, archivos, paquetes instalados)

Si el proceso del backend se reinicia
  → cache miss → ConnectSandbox(sandboxID) intenta reconectar
  → si el sandbox sigue vivo (dentro del TTL) → reconecta
  → si expiró → próximo uso crea uno nuevo

Al finalizar el chat (no implementado aún)
  → destroySandbox(chatID) → Kill() + remove from cache
```

---

## Outputs del code_interpreter

El code_interpreter retorna resultados MIME-typed:

| MIME type | Contenido | Frontend |
|-----------|-----------|----------|
| `text/plain` | Texto simple, resultado de print() | Mostrado en burbuja de mensaje |
| `text/html` | Tablas, charts matplotlib via mpld3 | Renderizado en canvas (iframe) |
| `image/png` | Matplotlib figures como PNG base64 | Renderizado en canvas (img tag) |
| `image/svg+xml` | Gráficos SVG | Renderizado en canvas |

El backend anota la presencia de rich outputs en el `tool_result`:
```
[html_output available — frontend will render]
[image output available — frontend will render]
[svg output available — frontend will render]
```

Cuando el frontend detecta estos marcadores, solicita el output rico al sandbox
(via el evento `sandbox_output`) y lo muestra en el canvas.

---

## Artifacts vs sandbox

Hay dos tipos de "canvas" en el frontend:

### 1. Artifacts estáticos (sin sandbox)
El LLM genera código HTML/JSX/SVG directamente en su respuesta. El frontend
detecta el bloque fenced y lo renderiza en un iframe sandbox **sin** llamar
al backend.

```
LLM responde:
  ```html
  <html>...</html>
  ```
Frontend detecta y renderiza en iframe.
```

### 2. Artifacts ejecutados (con sandbox)
El LLM usa `code_interpreter` para ejecutar código que produce output rico.
El output se muestra en el canvas.

```
LLM usa code_interpreter:
  python: import matplotlib; plt.plot([1,2,3]); plt.savefig("chart.png")
Backend: ejecuta en OpenSandbox → retorna image/png base64
Frontend: renderiza imagen en canvas
```

La diferencia: artifacts estáticos no necesitan OpenSandbox. Artifacts
ejecutados (análisis de datos, gráficos, código interactivo) sí.

---

## Seguridad

El `SafetyFilter` del Runtime inspecciona todos los tool calls **antes**
de llegar al sandbox:

```go
// En mode_provider.go:
WithSafety(ab.NewSafetyChain(
    ab.DefaultDangerousCommandFilter(),  // bloquea rm -rf /, dd of=/dev, etc.
    ab.DefaultSecretLeakFilter(),        // bloquea sk-ant-, ghp_, AKIA, etc.
))
```

Si un comando está bloqueado, el LLM recibe el mensaje de bloqueo y puede
auto-corregirse.

El sandbox mismo corre en un contenedor aislado — el código del usuario
no puede afectar el host del backend.

---

## Testing manual

```bash
# Con sandbox configurado:
export OPEN_SANDBOX_API_KEY=...
export OPEN_SANDBOX_DOMAIN=api.open-sandbox.ai
export ANTHROPIC_API_KEY=...

go run .

# Crear chat
curl -X POST http://localhost:8080/api/chats -H "Content-Type: application/json" \
  -d '{"title":"sandbox test"}'
# → {"id":1,...}

# Stream con código Python
curl -N -X POST http://localhost:8080/api/chats/1/stream \
  -H "Content-Type: application/json" \
  -d '{"prompt": "Run Python: print(2+2) and plot a simple chart"}'
# → event: tool_call  (code_interpreter invoked)
# → event: tool_result (stdout: "4")
# → event: sandbox_output (if rich output detected)
# → event: delta (LLM response)
# → event: done

# Sin sandbox (OPEN_SANDBOX_API_KEY vacío):
# → LLM no verá bash/code_interpreter tools
# → responde con texto explicando qué haría
```

---

## Próximo paso: Frontend canvas

El frontend necesita:

1. **Detectar bloques fenced** en los `delta` acumulados → renderizar en canvas
2. **Detectar evento `sandbox_output`** → renderizar rich outputs en canvas
3. **ArtifactCanvas component** → panel lateral derecho
4. **iframe sandbox** para HTML/JSX
5. **img/svg tags** para imágenes del code_interpreter

Ver `design.md` para el diseño visual del canvas.
