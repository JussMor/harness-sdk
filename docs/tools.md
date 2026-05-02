# Obvious — Modelo de Herramientas

Las herramientas son la única forma en que el agente produce efectos en el mundo. Sin herramientas, el agente solo genera texto. Con herramientas, puede leer datos, crear artifacts, ejecutar código, buscar en la web, enviar mensajes, y coordinar agentes. Este documento cubre cómo están organizadas, cuándo se usan, y las reglas que gobiernan su uso.

---

## Categorías de herramientas

### Workspace y artifacts

El grupo más usado. Todo lo que crea, modifica, o lee artifacts del proyecto.

**`explore-artifacts`** — Descubrimiento e inspección. Sin `artifactId` lista todos los artifacts del proyecto con metadata. Con `artifactId` hace análisis profundo: schema, datos de muestra, relaciones entre sheets. Es el punto de entrada para entender qué existe antes de actuar.

**`search-workspace`** — Búsqueda semántica dentro del contenido de artifacts. Busca en documentos, sheets, presentaciones, PDFs, threads pasados, y templates. Se diferencia de `explore-artifacts` en que busca *dentro* del contenido, no solo metadata. Scope `current` primero; si no hay resultados, `workspace` automáticamente.

**`document-operations`** — Crear y editar documentos markdown. Tres modos: `write` para crear o reemplazar completo, `edit-surgical` para search/replace exacto (preferido para edits), `edit-ai` para rewrites con instrucciones en lenguaje natural. `edit-surgical` es el modo correcto para la mayoría de edits — preserva tablas, comment marks, y formato exactamente.

**`sheet-operations`** — Modificar schema y metadata de sheets. Agrega, actualiza, o elimina campos. Cambia tipos, agrega opciones de enum, configura validaciones. Requiere checkpoint antes de usar.

**`run-sql-with-duck-db`** — SQL directo sobre sheets. DuckDB completo: JOINs, agregaciones, window functions, CTEs. Los IDs de sheets van entre comillas dobles como nombres de tabla. Puede guardar resultados en sheets nuevas o existentes. Requiere checkpoint antes de escribir.

**`eval-js`** — Transformaciones JavaScript sobre sheets. `map` para transformar fila por fila (mismo número de registros), `reduce` para filtrar, agrupar, o deduplicar (número de registros diferente). Siempre agregar columnas nuevas con `sheet-operations` antes de usar `eval-js` si se generan campos nuevos.

**`create-dashboard`** / **`update-dashboard`** — Dashboards con charts nativos Recharts conectados en tiempo real a sheets. Los charts se actualizan automáticamente cuando cambia la sheet. No requiere la skill `dataviz` — esa es solo para charts Plotly en documentos.

**`create-view-from-sheet`** — Crea vistas sobre sheets: kanban, calendar, timeline, checklist, gallery, form. Infiere campos automáticamente desde el schema.

**`delete`** — Soft delete de artifacts y sheets. Requiere checkpoint antes de usar. Los datos se preservan para rollback.

---

### Ejecución y cómputo

**`computer-ops`** — Ejecutar comandos shell en cualquier computadora registrada. `id: "obvious"` para el sandbox del proyecto. `id: "cmp_xxx"` para repo sandboxes o servidores remotos. Para trabajo en repos, siempre usar el `computerId` del repo — nunca `id: "obvious"` para código de un repo específico.

Los repo sandboxes tienen `defaultCwd` configurado — no hace falta prefixar con `cd /path/to/repo &&`. El sandbox tiene acceso a todo el toolchain del proyecto: Node, Python, Go, Bun, git, gh, curl, y cualquier dependencia instalable via apt/pip/npm.

**`run-shell`** — Alias simplificado para ejecutar en el sandbox obvio. Mismo resultado que `computer-ops` con `id: "obvious"` pero más conciso para operaciones rápidas.

**`isolate-sandbox`** — Convierte el sandbox compartido del thread en uno independiente. Útil cuando hay riesgo de colisión con otros threads. Los archivos del sandbox compartido no se copian — el sandbox aislado empieza limpio.

---

### Datos y SQL

**`fetch-large-file`** — Descarga el contenido real de archivos grandes (100MB+) que llegan al sandbox como stubs. Los stubs tienen metadata y una URL S3 presignada pero no el contenido. Usar cuando `find /home/user/project -name "*.stub"` encuentra archivos pendientes.

**`validate-data`** — Aplica reglas de validación sobre sheets y las renderiza en la UI. El campo `check` es un predicado de validez — se marca cuando es `false`. Para flagear `value === 'Austin'`, el check debe ser `value !== 'Austin'`. Modos: `append` (default), `update`, `replace`.

**`sheet-version-control`** — Lista versiones de una sheet y restaura a una versión anterior. Útil para deshacer transformaciones o recuperar datos antes de un `eval-js` destructivo.

---

### Web e investigación

**`web-operations`** — Tres modos: `search` (búsqueda web via Exa), `fetch` (contenido de URLs específicas), `crawl` (BFS por dominio). Para búsquedas de contenido reciente usar `searchType: "fast"` con filtros de fecha. Para descubrimiento conceptual, `neural`. Después de una búsqueda, hacer `fetch` en las URLs más relevantes para el texto completo.

La fecha actual es mayo 2026 y el training data tiene corte a finales de 2024 — siempre asumir que el conocimiento está desactualizado y verificar con búsqueda web antes de afirmar hechos sobre tecnologías, APIs, o precios.

**`get-file`** — Accede al contenido de archivos subidos por el usuario (PDFs, imágenes, CSVs, código). Para PDFs e imágenes devuelve el contenido directamente al modelo. Para otros tipos devuelve un `shellPath` y un `validationCommand` — ejecutar el validationCommand antes de leer el archivo.

---

### Planificación y coordinación

**`request-plan-approval`** — Propone un plan estructurado al usuario antes de ejecutar trabajo complejo. Usar cuando la tarea tiene 3+ fases distintas o hay múltiples caminos válidos. El usuario puede aprobar con auto-aprobación (el agente procede libremente) o aprobación por pasos (check-in en cada fase). Después de aprobación, guardar el plan en memoria del proyecto.

**`update-objective-status`** — Actualiza el estado de objetivos dentro de un plan aprobado. Los estados en cascada automáticamente hacia arriba en la jerarquía.

**`spawn-runner`** — Lanza un subthread autónomo para una tarea específica. Máximo 5 runners concurrentes. El runner recibe una descripción autocontenida y un `resourceBundle` con los artifacts que necesita. No tiene acceso al historial del thread padre. Tiers: `nano` para tareas simples de alto volumen, `mini` para razonamiento o código.

**`timers`** — Programa un mensaje para entregarse en el thread en un momento futuro. Útil para monitoreo asíncrono, follow-ups, y verificación de procesos en background. No bloquea la ejecución — el agente recibe el mensaje como un turno nuevo cuando el timer dispara.

---

### Comunicación externa

**`email-operations`** — Gmail y Outlook. Siempre `list_accounts` primero para obtener `connectorId`. `compose_email` muestra preview que el usuario edita y envía — no envía directamente. `send_email` solo cuando el usuario aprobó explícitamente el contenido.

**`slack-operations`** — Slack via bot del workspace. El bot debe unirse a canales públicos antes de postear (`join_channel`). Para canales privados, usar `invite`. Confirmar con el usuario antes de enviar a canales. `subscribe: true` en `send_message` para workflows interactivos que necesitan recibir respuestas.

**`calendar-operations`** — Google Calendar y Outlook. Confirmar con el usuario antes de crear, actualizar, o eliminar eventos. La eliminación no se puede deshacer.

**`notify-user`** — Notificación externa al usuario fuera de la conversación (SMS, Slack, email, llamada). Usar después de tareas largas, cuando hay un bloqueante que requiere input, o cuando el usuario lo solicita. No usar para actualizaciones rutinarias cuando el usuario está activo en la conversación.

---

### Integraciones y CRM

**`crm-operations`** — Agregador multi-CRM. Las operaciones de lectura (`search_contacts`, `search_companies`, `list_deals`) hacen fan-out a todos los CRMs conectados en paralelo. Las operaciones de escritura requieren `connectorId` específico. Para Salesforce: `query` acepta SOQL directo — siempre incluir `LIMIT`.

**`unified-passthrough`** — Llamadas directas a APIs nativas de proveedores usando conexiones OAuth existentes. Usar rutas nativas del proveedor (empezando con `/`), no rutas de unified.to. No setear headers de Authorization — el auth lo maneja la conexión.

**`get-available-credentials`** — Lista todas las conexiones e integraciones disponibles en el workspace. Punto de entrada antes de usar cualquier integración externa para verificar qué está conectado.

**`request-credentials`** — Solicita credenciales al usuario (API keys, secrets, OAuth). Los secrets quedan disponibles como `process.env.SECRET_{KEY}` en el sandbox.

---

### Checkpoints y seguridad

**`create-checkpoint`** — Snapshot del proyecto que permite rollback. **Obligatorio antes y después de cualquier modificación a artifacts.** No hay excepciones. El label debe describir qué se va a hacer o qué se hizo.

La secuencia correcta para cualquier cambio:

```
create-checkpoint("Antes de X")
→ ejecutar cambios
→ create-checkpoint("Después de X — [descripción]")
```

Sin checkpoint previo, no hay forma de deshacer si algo sale mal. Sin checkpoint posterior, el progreso no está protegido.

---

## Reglas de uso

### Paralelismo

Cuando múltiples herramientas no tienen dependencias entre sí, se invocan simultáneamente en el mismo bloque. Esto reduce el tiempo total de ejecución significativamente. La regla es: si el resultado de A no afecta los parámetros de B, A y B van en paralelo.

Ejemplo correcto — explorar artifacts y leer memoria en paralelo:

```
explore-artifacts()          ←┐ paralelo
memory({ path: "/" })        ←┘
```

Ejemplo incorrecto — necesita el ID del artifact antes de leer su contenido:

```
explore-artifacts()           ← primero
→ document-operations(artifactId=resultado)  ← después
```

### Herramientas que requieren checkpoint

Cualquier herramienta que modifica datos requiere checkpoint antes de ejecutarse:

- `document-operations` (write, edit-surgical, edit-ai)


- `sheet-operations`


- `run-sql-with-duck-db` (cuando guarda resultados)


- `eval-js`


- `delete`


- `validate-data` (mode: replace)



### Preferencia de herramientas para edits

Para editar documentos, la jerarquía es:

1. `edit-surgical` — para cualquier cambio donde se sabe exactamente qué texto reemplazar


2. `edit-ai` — solo cuando el agente necesita decidir *cómo* reescribir (condensar, reestructurar)


3. `write` completo — solo para crear desde cero o reemplazar todo el contenido



`edit-ai` puede alterar tablas, eliminar comment marks, y cambiar formato inesperadamente. `edit-surgical` no toca nada fuera del texto buscado.

### Herramientas que NO crean sandbox

Los artifacts nativos de Obvious no necesitan sandbox ni proceso activo:

- Documentos, workbooks, sheets


- Kanban, calendar, timeline, gallery, checklist


- Dashboards con charts Recharts


- Canvas (Excalidraw)


- Folios — sí crean un archivo HTML en `/project/folios/` pero no un proceso



Solo las apps web hosteadas con `register-hosted-service` necesitan un proceso vivo en el sandbox.

### Búsqueda antes que asunción

Antes de afirmar que algo no existe en el workspace, buscar:

```
search-workspace({ query: "...", projectScope: "current" })
→ si vacío: search-workspace({ query: "...", projectScope: "workspace" })
```

Antes de afirmar un hecho técnico sobre una API o tecnología:

```
web-operations({ operation: "search", query: "..." })
```

El training data tiene corte a finales de 2024. Para cualquier información que pueda haber cambiado — versiones, precios, APIs, features — verificar con búsqueda web.

---

## Herramientas por flujo de trabajo

### Análisis de datos

`explore-artifacts` → `run-sql-with-duck-db` → `create-dashboard` o `document-operations`

### Migración de datos

`explore-artifacts` → `run-sql-with-duck-db` (transform) → `sheet-operations` (schema) → `validate-data` → `eval-js` (si necesario)

### Investigación web

`web-operations(search)` × 3-5 → `web-operations(fetch)` en URLs clave → `document-operations(write)`

### Trabajo en código

`computer-ops(id: "cmp_xxx")` — siempre el computerId del repo, nunca `id: "obvious"` para código de repositorios

### Coordinación multi-agente

`request-plan-approval` → `create-checkpoint` → `spawn-runner` × N (paralelo) → consolidar resultados → `create-checkpoint`

### Comunicación externa

`get-available-credentials` → verificar conexión → `email-operations` / `slack-operations` / `calendar-operations`