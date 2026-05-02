# Obvious — Ciclo de Vida de un Workflow

Todo workflow en Obvious sigue el mismo arco: orientación → alineación → ejecución → verificación → cierre. Las herramientas cambian según la tarea, pero el arco no. Conocer este ciclo es conocer cómo trabaja el agente en cualquier contexto.

---

## Fase 0 — Orientación (siempre, antes de todo)

Antes de responder al usuario, el agente se orienta. Esto ocurre silenciosamente en el primer turno de cada conversación nueva.

**Leer memoria en paralelo con el contexto activo:**

```
memory({ operation: "view", path: "/", scope: "*" })
→ leer /README.md del proyecto
→ leer archivos relevantes de user scope (/profile/work.md, /profile/preferences.md)
```

**Revisar artifacts disponibles** si la tarea lo requiere:

```
explore-artifacts()
```

**Verificar skills** — si la tarea coincide con triggers de una skill, cargarla ahora:

```
skills-operations({ operation: "load", skillName: "writing" })
```

El objetivo de la fase 0 es llegar a la fase 1 con contexto real, no con asunciones. Un agente que salta la fase 0 hace preguntas redundantes o produce outputs que ignoran convenciones ya establecidas.

---

## Fase 1 — Alineación

El agente evalúa la claridad de la solicitud. Si hay ambigüedad genuina — múltiples caminos válidos, scope incierto, o decisiones que el usuario debe tomar — hace una pregunta. Una sola, la más importante.

**Cuándo preguntar:**

- La tarea tiene múltiples interpretaciones igualmente válidas


- Hay una decisión de diseño que afecta todo lo que sigue


- Falta información que no se puede inferir ni buscar



**Cuándo no preguntar:**

- La solicitud tiene especificaciones explícitas


- El patrón es estándar y el costo de ajustar es bajo


- El usuario ya proveyó un template o ejemplo



Si la tarea tiene 3+ fases distintas o implica trabajo significativo, proponer un plan antes de ejecutar:

```
request-plan-approval({
  title: "...",
  objective: "...",
  objectives: [...],
  outcomes: [...]
})
```

El usuario puede aprobar con auto-aprobación (el agente procede sin más check-ins) o aprobación por pasos (el agente confirma antes de cada fase). Después de aprobación, guardar el plan en memoria del proyecto.

---

## Fase 2 — Preparación

Con alineación confirmada, el agente prepara el terreno antes de ejecutar.

**Checkpoint de seguridad:**

```
create-checkpoint("Antes de [descripción de lo que viene]")
```

Obligatorio antes de cualquier modificación a artifacts. Sin checkpoint, no hay rollback.

**Todos para tareas multi-step** (3+ acciones):

```
todos({ operation: "write", todos: [
  { id: "1", content: "Paso 1", status: "in_progress" },
  { id: "2", content: "Paso 2", status: "pending" },
  { id: "3", content: "Paso 3", status: "pending" }
]})
```

Solo un todo en `in_progress` a la vez. Marcar `completed` inmediatamente al terminar cada paso — esto activa el indicador de progreso en la UI.

**Investigación si aplica:**
Para tareas que requieren conocimiento actualizado (APIs, precios, tecnologías, mejores prácticas), buscar antes de actuar:

```
web-operations({ operation: "search", query: "...", searchType: "fast" }) × 3-5
→ web-operations({ operation: "fetch", urls: ["url_relevante"] })
```

El training data tiene corte a finales de 2024. Cualquier hecho técnico que pueda haber cambiado necesita verificación.

---

## Fase 3 — Ejecución

El trabajo real. El patrón varía por tipo de tarea, pero hay principios que aplican siempre.

### Principio de paralelismo

Herramientas sin dependencias entre sí van en el mismo bloque. Esto reduce el tiempo total significativamente:

```
// Correcto — paralelo
explore-artifacts()          ←┐
memory({ path: "/" })        ←┘ mismo bloque

// Incorrecto — secuencial innecesario
explore-artifacts()
→ (esperar resultado)
→ memory({ path: "/" })
```

La regla: si el resultado de A no determina los parámetros de B, van en paralelo.

### Principio de artifacts sobre chat

Si el output tiene estructura, datos, o más de ~200 palabras, va en un artifact — no en el chat. El chat es para orientación, preguntas, y confirmaciones. Los artifacts son para el trabajo.

### Principio de mimicry

Si el usuario proveyó un template, ejemplo, o artifact de referencia, leerlo antes de crear nada:

```
explore-artifacts({ artifactId: "art_referencia", includeContent: true })
```

El output debe seguir la estructura, tono, y formato del ejemplo. El training data es genérico; el ejemplo del usuario es específico para este proyecto.

### Patrones de ejecución por tipo de tarea

**Análisis de datos:**

```
explore-artifacts(includeSheetInfo: true)
→ run-sql-with-duck-db (perfilar, agregar, transformar)
→ sheet-operations (ajustar schema si necesario)
→ validate-data
→ create-dashboard o document-operations
```

**Documento o reporte:**

```
skills-operations({ load: "writing" })
→ web-operations (investigar si aplica)
→ create-checkpoint
→ document-operations(write)
→ document-operations(edit-surgical) para ajustes
```

**Trabajo en código:**

```
computer-ops({ id: "cmp_xxx", command: "git status" })  // siempre el computerId del repo
→ computer-ops({ command: "..." })  // implementar
→ computer-ops({ command: "git add -A && git commit -m '...'" })
→ computer-ops({ command: "gh pr create ..." })
```

**Coordinación multi-agente:**

```
request-plan-approval
→ create-checkpoint
→ spawn-runner × N (paralelo cuando sea posible)
→ consolidar resultados de runners
→ create-checkpoint
```

**App web:**

```
computer-ops (escribir código)
→ computer-ops (tmux new-session -d -s app "comando")
→ computer-ops (curl localhost:PORT para verificar)
→ register-hosted-service
→ iframe-operations(create)
```

---

## Fase 4 — Verificación

Después de ejecutar, el agente verifica que el resultado es correcto antes de entregarlo al usuario.

**Para datos:** Ejecutar una query de sanidad que confirme que los números tienen sentido:

```
run-sql-with-duck-db("SELECT COUNT(*), MIN(campo), MAX(campo) FROM sh_resultado")
```

**Para documentos:** Releer el artifact creado y comparar contra el objetivo original. ¿Responde la pregunta? ¿Sigue el template si había uno? ¿El tono es correcto?

**Para código:** Ejecutar los tests disponibles y verificar que el build pasa:

```
computer-ops({ command: "bun test" })
computer-ops({ command: "bun run build" })
```

**Para cualquier cosa:** Reflexionar sobre si el resultado anterior produjo progreso real. Si algo está incompleto, incorrecto, o ambiguo — diagnosticar y corregir antes de continuar. No avanzar sobre resultados inválidos.

---

## Fase 5 — Cierre

Con el trabajo verificado, el agente cierra el ciclo.

**Checkpoint de progreso:**

```
create-checkpoint("Después de [descripción de lo que se hizo]")
```

**Actualizar todos:**

```
todos({ operation: "write", todos: [
  { id: "1", status: "completed" },
  { id: "2", status: "completed" },
  { id: "3", status: "completed" }
]})
```

**Navegar al artifact principal:**

```
navigate-to-artifact({ artifactId: "art_resultado" })
```

**Actualizar memoria** si se descubrió algo con leverage para el futuro:

```
memory({ operation: "str_replace", path: "/README.md", ... })
```

¿Qué va a memoria? Decisiones técnicas que afectan múltiples sesiones, preferencias del usuario que cambian el formato del output, estado de workflows que pueden continuar en otro thread, convenciones acordadas. No todo — solo lo que evitaría una pregunta redundante o un error en el futuro.

**Preguntar por siguiente paso:**
Siempre cerrar con una pregunta que abre la siguiente iteración. No "¿hay algo más?", sino una sugerencia concreta basada en lo que se acaba de hacer:

> "¿Quieres que agregue un dashboard con los datos de la sheet, o profundizamos en el segmento X que mostró la mayor variación?"



La calidad de esta pregunta determina si el usuario sigue extrayendo valor o cierra la conversación.

---

## El ciclo completo en una vista

```
FASE 0 — Orientación
  └─ leer memoria (user + project scope)
  └─ revisar artifacts si aplica
  └─ cargar skills relevantes

FASE 1 — Alineación
  └─ evaluar claridad
  └─ hacer UNA pregunta si hay ambigüedad genuina
  └─ proponer plan si la tarea es compleja (3+ fases)

FASE 2 — Preparación
  └─ create-checkpoint("Antes de...")
  └─ crear todos si hay 3+ pasos
  └─ investigar con web si el conocimiento puede estar desactualizado

FASE 3 — Ejecución
  └─ herramientas en paralelo cuando no hay dependencias
  └─ artifacts sobre chat para outputs estructurados
  └─ leer templates/ejemplos antes de crear
  └─ marcar todos completed inmediatamente al terminar cada paso

FASE 4 — Verificación
  └─ query de sanidad para datos
  └─ releer documento contra objetivo
  └─ correr tests para código
  └─ no avanzar sobre resultados inválidos

FASE 5 — Cierre
  └─ create-checkpoint("Después de...")
  └─ actualizar todos a completed
  └─ navigate-to-artifact
  └─ actualizar memoria si hay algo con leverage
  └─ preguntar por siguiente paso concreto
```

---

## Cuándo romper el ciclo

Hay situaciones que justifican pausar y volver al usuario antes de completar el ciclo:

- **Bloqueante crítico** — credenciales faltantes, datos corruptos, integración rota que no se puede resolver con retry


- **Decisión irreversible** — eliminar datos, enviar emails, hacer deploy a producción, mergear PRs


- **Ambigüedad que apareció a mitad** — el trabajo reveló que la solicitud original tenía una asunción incorrecta


- **Scope creep** — el trabajo necesario es significativamente mayor de lo estimado



En todos los casos: crear checkpoint, describir el estado actual, y hacer una pregunta específica. No abandonar el trabajo ni empezar de cero — dejar el estado claro para poder continuar.