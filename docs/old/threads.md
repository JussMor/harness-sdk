# Obvious — Modelo de Comunicación, Threads y Skills

Obvious no es un agente único. Es un sistema de threads coordinados que comparten un workspace, se comunican por mensajes, y cargan conocimiento especializado bajo demanda. Este documento cubre cómo funciona ese sistema desde adentro.

---

## Threads: la unidad de ejecución

Cada conversación en Obvious es un **thread**. Un thread tiene un ID (`th_xxx`), pertenece a un proyecto (`prj_xxx`), y puede tener un modo asignado (balanced, analyst, deep). Dentro de un thread vive el historial de mensajes, los todos activos, y el contexto de ejecución del agente.

Los threads no son aislados. Comparten el workspace de artifacts del proyecto — cualquier thread puede leer y escribir los mismos documentos, sheets y workbooks. Lo que no comparten es el estado del sandbox: cada thread tiene acceso al mismo sandbox del proyecto (`id: "obvious"`), pero los procesos activos (apps web, scripts corriendo) son específicos del sandbox, no del thread.

### Tipos de thread

**Thread principal** — El que el usuario abre directamente. Es el orchestrator cuando hay trabajo complejo que delegar.

**Runner (subthread)** — Spawneado por el orchestrator con `spawn-runner`. Ejecuta una tarea específica y reporta de vuelta. Tiene acceso a las mismas herramientas que el thread principal. Hay un máximo de 5 runners concurrentes por orchestrator.

**Child thread con objetivo** — Creado cuando un task tiene `parentThreadId`. El child reporta su estado al parent via `report-objective-status`.

**Thread de tarea programada** — Ejecutado por el scheduler cuando un task tiene un trigger de schedule o webhook. Corre de forma completamente autónoma, sin interacción con el usuario.

---

## Comunicación entre threads

### spawn-runner

El patrón más común para paralelismo. El orchestrator lanza runners para tareas independientes:

```
spawn-runner({
  tier: "nano" | "mini",
  task: "descripción específica y autónoma",
  resourceBundle: [{ id: "sh_xxx", type: "sheet", description: "..." }]
})
```

**Tiers:**

- `nano` — GPT-5.4 nano. Tareas simples, alto volumen, velocidad y costo mínimo.


- `mini` — GPT-5.4 mini. Tareas que requieren razonamiento, código, o juicio.



Los runners son **fire-and-forget desde el orchestrator** — no se hace polling. El runner notifica automáticamente cuando termina. El orchestrator recibe el resultado como un evento en su thread.

El runner tiene acceso completo a herramientas: puede leer/escribir artifacts, ejecutar SQL, correr shell, hacer búsquedas web. Lo que no tiene es contexto de la conversación del thread padre — la tarea debe ser completamente autocontenida.

### thread-messaging

Para comunicación directa entre threads conocidos:

```
thread-messaging({
  operation: "send",
  threadId: "th_xxx",
  message: "...",
  deliveryMode: "interjected" | "queued"
})
```

- `interjected` (default) — entrega el mensaje después del tool call actual en curso.


- `queued` — entrega después de que termina la ejecución completa del thread receptor.



Los threads autobuild/code-employee pueden enviar mensajes a cualquier otro thread del mismo tipo en el workspace, no solo a parent/child. Esto permite coordinación horizontal entre agentes especializados.

**Importante:** el mensaje se entrega usando el modo del thread **receptor**, no el del emisor.

### report-objective-status

Solo disponible en threads spawneados con un objetivo. El child reporta al parent:

```
report-objective-status({
  status: "success" | "failure" | "input-required" | "pending",
  summary: "...",
  result: { ... }  // debe conformar al outputSchema si fue definido
})
```

`pending` resetea el estado a in-progress (útil cuando el child había pedido input y el usuario respondió). Los otros estados terminan la ejecución del child.

---

## Sandboxes y threads

Un sandbox no es un thread. La relación es:

| Concepto | Descripción |
| --- | --- |
| Thread | Conversación + historial + todos + contexto |
| Sandbox (`id: "obvious"`) | Entorno de ejecución del proyecto — filesystem, procesos, red |
| Repo sandbox (`id: "cmp_xxx"`) | Entorno dedicado por repositorio — no comparte archivos con el sandbox principal |

Múltiples threads del mismo proyecto pueden ejecutar comandos en el mismo sandbox `id: "obvious"`. Esto significa que pueden haber colisiones si dos threads escriben al mismo path simultáneamente. Para evitarlo, un thread puede aislarse con `isolate-sandbox` — esto le da un sandbox independiente, pero los archivos del sandbox compartido **no se copian automáticamente**.

Los repo sandboxes (`cmp_xxx`) son entornos separados por repositorio. Cuando se trabaja con código de un repo específico, los comandos deben ir al `computerId` de ese repo, no a `id: "obvious"`.

---

## Skills: conocimiento bajo demanda

Las skills son archivos de instrucciones especializadas que el agente carga en su contexto activo. No son código ejecutable — son guías de comportamiento, patrones, y reglas específicas para un dominio.

### Por qué existen

El training data del modelo es genérico y tiene corte a finales de 2024. Las skills contienen patrones específicos del proyecto, convenciones del equipo, y workflows que no existen en ningún dataset de entrenamiento. Cuando hay conflicto entre lo que el modelo "sabe" y lo que dice una skill, **la skill gana**.

### Cómo se cargan

```
skills-operations({ operation: "load", skillName: "writing" })
```

Una skill cargada se inyecta en el contexto persistente del agente — aparece en cada turno mientras esté activa. No desaparece entre mensajes como lo haría un tool result normal.

```
skills-operations({ operation: "unload", skillName: "writing" })
```

Se descarga cuando ya no es necesaria, para liberar espacio de contexto.

Las skills custom del usuario siguen el mismo patrón con el prefijo `user-`:

```
skills-operations({ operation: "load", skillName: "user-cutlist-domain-model" })
```

Si el skill loader no está disponible, los archivos se pueden leer directamente:

```
cat /home/user/skills/user-{name}/SKILL.md
```

### Regla de activación

Si una tarea coincide con los **triggers** de una skill, el agente la carga **antes de tomar cualquier acción**. No después de empezar, no durante — antes. Esta es una regla sin excepciones porque el patrón de fallo más común es asumir que el training data es suficiente cuando la skill tiene instrucciones específicas que lo contradicen.

### Skills del sistema disponibles

| Skill | Dominio | Triggers clave |
| --- | --- | --- |
| `writing` | Documentos y redacción | writing, document, clarity, rewrite |
| `folio-builder` | Presentaciones web HTML | folio, landing page, report, memo |
| `dataviz` | Charts Plotly en documentos | chart, plotly, visualization, graph |
| `canvas-builder` | Diagramas Excalidraw | canvas, diagram, flowchart, wireframe |
| `slide-presentations` | Slides y decks | slides, presentation, deck, powerpoint |
| `data-migration` | ETL y transformación | migration, ETL, schema mapping, import |
| `web-hosting` | Apps web hosteadas | web server, hosting, public url, iframe |
| `web-design` | Design system y UI | build website, UI, HTML CSS, design system |
| `external-integrations` | APIs externas | API, OAuth, pagination, rate limit |

### Skills custom (usuario)

Las skills custom se crean para un usuario o proyecto específico. En este proyecto existen skills para el dominio completo de CutList Pro: modelo de dominio, optimizador guillotina, pricing engine, preview isométrico, reglas por rol, editor de taller, API del worker, integración Odoo, y cliente API. Cada una tiene sus propios triggers y se carga solo cuando la tarea lo requiere.

---

## Memoria: contexto que persiste

La memoria no es un artifact — es un sistema de archivos markdown que persiste entre conversaciones y proyectos.

### Dos scopes

**User scope** — Cross-proyecto. Preferencias de comunicación, estilo de trabajo, contexto profesional del usuario. Se consulta en cada conversación nueva.

**Project scope** — Específico del proyecto. Schemas de datos, decisiones técnicas, convenciones acordadas, estado de workflows. Se consulta cuando el agente trabaja en ese proyecto.

### Cuándo se actualiza

La memoria se actualiza cuando se descubren hechos de alto valor: una decisión técnica importante, una preferencia del usuario que cambia el output, un schema que se usará repetidamente, o contexto que evitaría preguntas redundantes en el futuro.

No todo va a memoria — solo lo que tiene leverage real para futuras conversaciones.

---

## Tareas programadas: threads autónomos

Un task puede tener un trigger de schedule (rrule) o webhook. Cuando se dispara, crea un thread nuevo que ejecuta los pasos del task de forma completamente autónoma.

Reglas para tasks programadas:

**Sin preguntas al usuario.** El thread no puede interactuar — debe resolver cualquier ambigüedad con la información disponible o fallar con un error claro.

**Idempotencia obligatoria.** Cada ejecución empieza sin memoria de runs anteriores. El agente debe explorar artifacts existentes al inicio para no crear duplicados. Escribir a `/project/` sin IDs canónicos en el path genera artifacts nuevos en cada run.

**Reintentos proactivos.** Rate limits, errores de API, y problemas de código se resuelven con retry — no se escalan al usuario salvo que sea un bloqueante crítico (credenciales rotas, pérdida de datos potencial, datos corruptos).

---

## Flujo completo: orchestrator + runners

El patrón más eficiente para trabajo complejo en Obvious:

1. El thread principal explora artifacts, lee skills, alinea con el usuario.


2. Propone un plan con `request-plan-approval` si hay 3+ fases.


3. Crea checkpoint.


4. Lanza runners paralelos para subtareas independientes (`spawn-runner`).


5. Ejecuta directamente las tareas que tienen dependencias secuenciales.


6. Recibe resultados de runners como eventos en el thread principal.


7. Consolida resultados, crea checkpoint final, navega al artifact entregado.


8. Actualiza memoria con hechos de alto valor descubiertos.



Los runners no tienen acceso al historial del thread padre. Todo el contexto que necesitan debe estar en el campo `task` o en `resourceBundle`. Si un runner necesita input adicional, reporta `input-required` al parent.