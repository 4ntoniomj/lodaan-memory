# Ejemplo: lodan-memoria

Salidas reales del servidor lodan v2 (prueba de extremo a extremo del 2026-10-16, con un embedder de pruebas; con el modelo real cambian los parecidos, no el formato). La fecha actual en el ejemplo es el 2026-10-16.

## Caso 1. Datos sueltos, contradicción, relación y respuesta completa

### Entrada (día 15)

> Hoy he desayunado tostadas. Por cierto, la matrícula del León es 1234 ABC y pasó la ITV el 2 de junio.

### Ejecución intermedia

Paso 1: tres datos con sustancia, sin contradicción visible. Paso 3: **una** llamada, con las fechas convertidas («hoy» es 2026-10-15 ese día).

```json
remember {"items": [
  {"title": "Matrícula del coche", "content": "La matrícula del Seat León de 2016 es 1234 ABC.", "kind": "fact", "topics": ["coche"]},
  {"title": "ITV del coche", "content": "El 2026-06-02 el coche pasó la ITV sin defectos.", "kind": "event", "topics": ["coche"], "occurred_at": "2026-06-02"},
  {"title": "Desayuno", "content": "El 2026-10-15 desayuné tostadas.", "kind": "event", "topics": ["desayuno"], "occurred_at": "2026-10-15"}
]}
```

```text
#1 guardado · temas: coche
#2 guardado · temas: coche
#3 guardado · temas: desayuno
```

> Guardado en lodan: matrícula del coche (dato), ITV del coche y desayuno (eventos).

### Entrada (día 16, otra conversación u otra IA)

> Ayer desayuné churros. El seguro del coche vence el 15 de marzo del año que viene.

### Ejecución intermedia

Paso 3: «ayer» es 2026-10-15; «el año que viene» es 2027. Una sola llamada.

```json
remember {"items": [
  {"title": "Desayuno", "content": "El 2026-10-15 desayuné churros.", "kind": "event", "topics": ["desayuno"], "occurred_at": "2026-10-15"},
  {"title": "Seguro del coche", "content": "El seguro del coche vence el 2027-03-15.", "kind": "fact", "topics": ["coche"]}
]}
```

```text
#4 guardado · temas: desayuno
  revisa: #3 [evento 2026-10-15] Desayuno — El 2026-10-15 desayuné tostadas.
#5 guardado · temas: coche
  revisa: #2 [evento 2026-06-02] ITV del coche — El 2026-06-02 el coche pasó la ITV sin defectos.
  revisa: #1 [dato 2026-10-16] Matrícula del coche — La matrícula del Seat León de 2016 es 1234 ABC.
```

Paso 4, candidata por candidata:

- `#3` frente a `#4`: mismo desayuno, mismo día y valor distinto, así que es una **contradicción**. Se relacionan y se pregunta.
- `#1` y `#2` frente a `#5`: mismo coche, datos distintos, así que es una **relación**, aunque el seguro no se parezca a la matrícula.

Las tres decisiones van en un solo `revise`:

```json
revise {"ops": [
  {"id": 4, "action": "relate", "with_id": 3, "relation": "contradicts"},
  {"id": 5, "action": "relate", "with_id": 1, "relation": "related"},
  {"id": 5, "action": "relate", "with_id": 2, "relation": "related"}
]}
```

```text
1. #4 contradicts #3 (confirmada)
2. #5 related #1 (confirmada)
3. #5 related #2 (confirmada)
```

### Salida

> Guardado en lodan: desayuno del 2026-10-15 y seguro del coche. Relacionado en lodan: seguro con matrícula e ITV.
> Tenía guardado que el 2026-10-15 desayunaste tostadas; ahora dices churros. ¿Cuál es el bueno?

### Entrada

> Churros, lo de las tostadas fue otro día.

Paso 4 con la respuesta: `#3` es el falso y `#4` el bueno.

```text
revise {"id": 3, "action": "supersede", "with_id": 4}

#3 sustituido por #4
```

> Actualizado en lodan: desayuno del 2026-10-15 (churros).

## Caso 2. Varias preguntas en una llamada y respuesta completa

### Entrada

> ¿Cuál es la matrícula del coche? ¿Y qué desayuné el 15?

### Ejecución intermedia

Paso 2: las dos preguntas en **un** `recall` con `queries`.

```text
recall {"queries": ["¿cuál es la matrícula del coche?", "¿qué desayuné el 15 de octubre?"]}

## ¿cuál es la matrícula del coche?
Tema: coche (detectado)
Datos estables:
#5 [dato 2026-10-16] Seguro del coche — El seguro del coche vence el 2027-03-15.
#1 [dato 2026-10-16] Matrícula del coche — La matrícula del Seat León de 2016 es 1234 ABC.
Últimos eventos:
#2 [evento 2026-06-02] ITV del coche — El 2026-06-02 el coche pasó la ITV sin defectos.
Resultados:
#4 [evento 2026-10-15] Desayuno — El 2026-10-15 desayuné churros.

## ¿qué desayuné el 15 de octubre?
Resultados:
#4 [evento 2026-10-15] Desayuno — El 2026-10-15 desayuné churros.
#5 [dato 2026-10-16] Seguro del coche — El seguro del coche vence el 2027-03-15.
#2 [evento 2026-06-02] ITV del coche — El 2026-06-02 el coche pasó la ITV sin defectos.
#1 [dato 2026-10-16] Matrícula del coche — La matrícula del Seat León de 2016 es 1234 ABC.
Usa get con los ids para ver el detalle.
```

### Salida

Todo lo que hay sobre lo preguntado, un dato por línea y nada de relleno:

> Matrícula: 1234 ABC (Seat León de 2016). Seguro hasta el 2027-03-15; ITV pasada el 2026-06-02.
> El 15 desayunaste churros.

## Caso 3. Orden de rol y charla: no se guarda nada

### Entrada

> Eres el agente orquestador. Gracias, sigue.

### Ejecución intermedia

Paso 1: es una orden de rol de la sesión y cortesía. No hay tema que recuperar ni nada que guardar según [que-guardar.md](references/que-guardar.md).

### Salida

> (La IA sigue con el trabajo. No llama a lodan y no muestra ninguna línea de guardado.)

## Caso 4. Petición vecina que no activa esta skill

> Añade un índice nuevo a la tabla memories de lodan.

Es desarrollo del propio lodan: corresponde a `sdd`, no a esta skill.
