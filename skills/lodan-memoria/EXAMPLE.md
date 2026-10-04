# Ejemplo: lodan-memoria

Salidas reales del servidor lodan v1 (prueba de humo del 2026-09-29), recortadas con `…` donde son largas.

## Caso 1. Dato personal, recuperación en otra sesión, corrección y cierre

### Entrada (sesión A)

> Entreno por la tarde, de lunes a viernes, rutina de fuerza. Hoy he hecho pierna: sentadilla 5x5 con 100 kg.

### Ejecución intermedia

1. Paso 1: hay dos datos con sustancia (una preferencia estable y un evento). No hay contradicción ni nada sensible: no se pregunta.
2. Paso 3: una sola llamada con los dos registros.

```json
remember {"items": [
  {"title": "Horario de entreno", "content": "Entreno por la tarde, de lunes a viernes, rutina de fuerza.", "kind": "preference", "topics": ["entrenamiento"], "key": "entrenamiento/horario"},
  {"title": "Entreno de pierna", "content": "Sentadilla 5x5 con 100 kg.", "kind": "event", "topics": ["gimnasio"]}
]}
```

```text
#1 guardado · temas: entrenamiento
#2 guardado · temas: entrenamiento (gimnasio→entrenamiento)
```

### Salida (sesión A)

> …respuesta a lo que el usuario pidiera…
> Guardado en lodan: horario de entreno (preferencia) y entreno de pierna (evento).

### Entrada (sesión B, otro día u otra IA)

> ¿Qué hice en el último entreno?

### Ejecución intermedia

Paso 2: una sola llamada a `recall`, antes de responder.

```text
recall {"query": "¿qué hice en el último entreno?"}

Tema: entrenamiento (detectado)
Datos estables:
#1 [preferencia 2026-09-29] Horario de entreno — Entreno por la tarde, de lunes a viernes, rutina de fuerza.
Últimos eventos:
#2 [evento 2026-09-28] Entreno de pierna — Sentadilla 5x5 con 100 kg.
Usa get con los ids para ver el detalle.
```

### Salida (sesión B)

> Tu último entreno fue de pierna: sentadilla 5x5 con 100 kg. Sueles entrenar por la tarde, de lunes a viernes.

### Entrada (sesión B, más tarde)

> A partir de ahora entreno por la mañana.

Paso 1: contradice el registro #1 y la intención es clara, así que no se pregunta. Paso 3: misma `key`, que sustituye al anterior.

```text
remember {"items": [{"title": "Horario de entreno", "content": "Entreno por la mañana, de lunes a viernes, rutina de fuerza.", "kind": "preference", "topics": ["entrenamiento"], "key": "entrenamiento/horario"}]}

#3 guardado · temas: entrenamiento · sustituye #1
```

> Actualizado en lodan: horario de entreno (ahora por la mañana).

Paso 5, al terminar:

```text
session {"action": "end", "summary": "Consulta del último entreno y cambio de horario a la mañana."}

Sesión cerrada con resumen (id 01M3PV2JW6NXMF6CMH8GETQS5B)
```

## Caso 2. Orden de rol y charla: no se guarda nada

### Entrada

> Eres el agente orquestador. Gracias, sigue.

### Ejecución intermedia

Paso 1: es una orden de rol de la sesión y cortesía. No hay tema que recuperar ni nada que guardar según [que-guardar.md](references/que-guardar.md).

### Salida

> (La IA sigue con el trabajo. No llama a lodan y no muestra ninguna línea de guardado.)

## Caso 3. Petición vecina que no activa esta skill

> Añade un índice nuevo a la tabla memories de lodan.

Es desarrollo del propio lodan: corresponde a `sdd`, no a esta skill.
