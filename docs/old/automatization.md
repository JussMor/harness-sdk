# Obvious — Tasks y Automatización

Las Tasks son el sistema de automatización de Obvious. Una Task es un workflow reutilizable guardado a nivel de proyecto: una secuencia de steps con instrucciones, condiciones de branching, gates de aprobación, y opcionalmente un trigger de schedule o webhook. Cuando se ejecuta, el agente sigue los steps como si fueran instrucciones directas del usuario.

---

## Qué es una Task

Una Task tiene tres componentes esenciales: un nombre, una descripción de cuándo usarla, y un array de steps ordenados. Cada step tiene un `id`, un `content` con las instrucciones para el agente, y una `position` (0-indexed).

```javascript
tasks({
  operation: "create",
  name: "Data Quality Check",
  description: "Analiza la calidad de datos en el workbook principal y genera un reporte.",
  steps: [
    { id: "profile", content: "Explora todos los sheets del proyecto y genera estadísticas de calidad: nulls, duplicados, tipos incorrectos.", position: 0 },
    { id: "report",  content: "Crea un documento con los hallazgos y recomendaciones priorizadas.", position: 1 }
  ]
})
```

El agente ejecuta los steps en orden usando todas sus herramientas normales. Una Task no limita qué puede hacer el agente — solo estructura qué debe hacer y en qué orden.

---

## Ejecutar una Task

```javascript
task-run({ taskId: "tsk_xxx" })
```

Esto reemplaza el todo list activo del agente con los steps de la Task y arranca el primer step en `in_progress`. El agente ejecuta autónomamente hasta completar todos los steps o encontrar un gate que requiera intervención humana.

Para encontrar el `taskId` antes de ejecutar:

```javascript
tasks({ operation: "list" })
```

---

## Steps avanzados

### nextStepId — control de flujo explícito

Por defecto los steps avanzan en orden de `position`. Para romper ese orden:

```javascript
{ id: "check",    content: "...", position: 0, nextStepId: "validate" }  // salta al step "validate"
{ id: "validate", content: "...", position: 1, nextStepId: null }         // null = terminar aquí
```

`nextStepId: null` termina el workflow después de ese step aunque haya más steps definidos.

### outputSchema — validar el output de un step

Si un step debe producir un output estructurado, se define un JSON Schema que el agente debe cumplir. Si el output no lo cumple, la Task falla en ese step.

```javascript
{
  id: "analyze",
  content: "Analiza las ventas y devuelve un resumen estructurado.",
  position: 0,
  outputSchema: {
    type: "object",
    required: ["totalRevenue", "topRegion", "riskLevel"],
    properties: {
      totalRevenue: { type: "number" },
      topRegion:    { type: "string" },
      riskLevel:    { type: "string", enum: ["low", "medium", "high"] }
    }
  }
}
```

Los output schemas son especialmente útiles cuando el siguiente step necesita consumir el resultado del anterior de forma confiable.

---

## Gates — aprobación humana en el flujo

Un gate bloquea la ejecución de un step hasta que se resuelva. Hay tres tipos.

### approval — requiere aprobación explícita

El agente pausa, renderiza un `GateApprovalCard` en el chat con botones Aprobar/Rechazar, y espera. Solo los `approvers` listados pueden resolver el gate.

```javascript
{
  id: "deploy",
  content: "Hace deploy a producción.",
  position: 2,
  gate: {
    type: "approval",
    approvers: ["junior@everbetter.com"],
    onReject: "abort"   // opciones: "abort" | "retry" | "route_to_step"
  }
}
```

`onReject: "route_to_step"` requiere también `rejectTargetStepId` para indicar a qué step ir si se rechaza.

### timeout — aprobación automática por tiempo

Si nadie actúa antes de `timeoutMinutes`, el gate se aprueba automáticamente.

```javascript
gate: {
  type: "timeout",
  timeoutMinutes: 60,
  onReject: "abort"
}
```

### auto_condition — gate basado en el output del step

Se evalúa el campo `condition` del step. Si la condición se cumple el gate se aprueba y avanza. Si no, ejecuta `onReject`.

```javascript
{
  id: "risk_check",
  content: "Evalúa el nivel de riesgo de la operación.",
  position: 1,
  gate: {
    type: "auto_condition",
    onReject: "route_to_step",
    rejectTargetStepId: "manual_review"
  },
  condition: {
    field: "riskLevel",
    operator: "equals",
    value: "low",
    ifTrue: "execute",
    ifFalse: "manual_review"
  }
}
```

**Regla crítica:** `auto_condition` y `condition` siempre van juntos en el mismo step. No funciona uno sin el otro.

---

## Conditions — branching sin gate

Una `condition` sin `gate` es branching puro: evalúa el output del step y enruta a diferentes steps según el resultado, sin bloquear ni pedir aprobación.

```javascript
{
  id: "classify",
  content: "Clasifica el ticket como urgente, normal, o bajo.",
  position: 0,
  condition: {
    field: "priority",
    operator: "equals",
    value: "urgent",
    ifTrue:  "escalate",
    ifFalse: "standard_flow"
  }
}
```

Operadores disponibles: `equals`, `not_equals`, `contains`, `not_contains`, `greater_than`, `less_than`, `exists`, `not_exists`.

El campo `field` usa dot notation para acceder a objetos anidados: `"metrics.errorRate"`, `"user.tier"`.

---

## Triggers — ejecución automática

### Schedule — tiempo

```javascript
tasks({
  operation: "create",
  name: "Weekly Report",
  description: "...",
  steps: [...],
  trigger: {
    type: "schedule",
    enabled: true,
    rrule: "FREQ=WEEKLY;BYDAY=MO;BYHOUR=9;BYMINUTE=0",
    timezone: "America/Guayaquil"
  }
})
```

El campo `rrule` sigue el estándar RFC 5545. Siempre incluir `FREQ`, `BYHOUR`, y `BYMINUTE`. Ejemplos comunes:

| Frecuencia | rrule |
| --- | --- |
| Diario a las 9am | `FREQ=DAILY;BYHOUR=9;BYMINUTE=0` |
| Lunes y viernes a las 8:30am | `FREQ=WEEKLY;BYDAY=MO,FR;BYHOUR=8;BYMINUTE=30` |
| Primer día del mes | `FREQ=MONTHLY;BYMONTHDAY=1;BYHOUR=0;BYMINUTE=0` |
| Cada día de semana | `FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=9;BYMINUTE=0` |

Para activar o desactivar sin cambiar el rrule:

```javascript
tasks({ operation: "update", taskId: "tsk_xxx", trigger: { type: "schedule", enabled: false, rrule: "...", timezone: "..." } })
```

Para eliminar el trigger completamente:

```javascript
tasks({ operation: "update", taskId: "tsk_xxx", trigger: null })
```

### Webhook — evento externo

```javascript
// Paso 1: crear la suscripción
webhooks({ operation: "create", setupMethod: "url", resourceName: "Pylon Support", events: ["created"] })
// → devuelve subscriptionId: "wsub_xxx"

// Paso 2: referenciar en la Task
tasks({
  operation: "create",
  name: "Process Support Ticket",
  steps: [...],
  trigger: { type: "webhook", subscriptionId: "wsub_xxx" }
})
```

Cuando el webhook recibe un evento, la Task se ejecuta con el payload disponible en el contexto del agente como `$WEBHOOK_PAYLOAD`.

---

## Tasks programadas — reglas de ejecución autónoma

Cuando una Task corre por schedule o webhook, el agente trabaja completamente solo. Sin preguntas, sin confirmaciones, sin interacción con el usuario. Las reglas son:

**Idempotencia obligatoria.** Cada ejecución empieza sin memoria de runs anteriores. Antes de crear cualquier artifact, verificar si ya existe:

```javascript
explore-artifacts()  // ¿ya existe el reporte de esta semana?
```

Escribir sobre el artifact existente, no crear uno nuevo cada vez. Crear artifacts nuevos en cada run llena el proyecto de duplicados.

**Reintentos proactivos.** Rate limits, errores de red, y errores de API transitorios deben reintentarse automáticamente con backoff. Solo pausar para el usuario cuando el bloqueante es estructural: credenciales rotas, datos corruptos, integración completamente caída.

**Usar `parentThreadId` para tasks que reportan a un thread padre:**

```javascript
tasks({
  operation: "create",
  name: "...",
  steps: [...],
  parentThreadId: "th_xxx",
  objective: "Generar el reporte semanal y guardarlo en el proyecto."
})
```

---

## Shortcuts vs Tasks

|   | Shortcuts | Tasks |
| --- | --- | --- |
| **Qué son** | Templates de prompt accesibles via `/` en el chat | Workflows multi-step con lógica de ejecución |
| **Cómo se usan** | El usuario los selecciona y edita antes de enviar | El agente los ejecuta autónomamente |
| **Lógica** | Ninguna — solo texto | Steps, gates, conditions, schedules, webhooks |
| **Cuándo usar** | Prompts frecuentes que el usuario repite | Procesos que deben correr consistentemente |

Crear un shortcut:

```javascript
shortcuts({ operation: "create", title: "Resumen ejecutivo", prompt: "Resume este documento en 3 puntos clave para un CEO." })
```

---

## Flujo completo: Task con branching y gate

Este ejemplo procesa tickets de soporte con clasificación automática, aprobación para casos urgentes, y dos paths de resolución:

```javascript
tasks({
  operation: "create",
  name: "Process Support Ticket",
  description: "Clasifica y resuelve tickets. Escala automáticamente los urgentes.",
  steps: [
    {
      id: "classify",
      content: "Analiza el ticket y clasifica su prioridad como urgent, normal, o low. Devuelve un objeto con campo 'priority'.",
      position: 0,
      outputSchema: {
        type: "object",
        required: ["priority"],
        properties: { priority: { type: "string", enum: ["urgent", "normal", "low"] } }
      },
      condition: {
        field: "priority",
        operator: "equals",
        value: "urgent",
        ifTrue: "escalate_gate",
        ifFalse: "auto_resolve"
      }
    },
    {
      id: "escalate_gate",
      content: "Prepara un resumen del ticket urgente para revisión del equipo.",
      position: 1,
      gate: {
        type: "approval",
        approvers: ["junior@everbetter.com"],
        onReject: "route_to_step",
        rejectTargetStepId: "auto_resolve"
      },
      nextStepId: "manual_resolve"
    },
    {
      id: "manual_resolve",
      content: "Resuelve el ticket con protocolo de escalación. Notifica al usuario por email.",
      position: 2,
      nextStepId: null
    },
    {
      id: "auto_resolve",
      content: "Resuelve el ticket automáticamente. Actualiza el sheet de tickets.",
      position: 3,
      nextStepId: null
    }
  ]
})
```

El flujo resultante:

```
classify
  ├─ urgent → escalate_gate
  │     ├─ aprobado   → manual_resolve → FIN
  │     └─ rechazado  → auto_resolve   → FIN
  └─ no urgent → auto_resolve → FIN
```

---

## Operaciones disponibles

```javascript
tasks({ operation: "list" })                           // listar todas las tasks del proyecto
tasks({ operation: "get",    taskId: "tsk_xxx" })      // detalle de una task
tasks({ operation: "update", taskId: "tsk_xxx", ... }) // actualizar steps, trigger, nombre
tasks({ operation: "delete", taskId: "tsk_xxx" })      // eliminar
task-run({ taskId: "tsk_xxx" })                        // ejecutar desde el inicio
task-run({ taskId: "tsk_xxx", stepId: "step_id" })     // reanudar desde un step específico
```

El usuario puede editar Tasks directamente desde las cards en el chat. El estado en el UI es la fuente de verdad — no asumir que el estado es el que el agente configuró por última vez.