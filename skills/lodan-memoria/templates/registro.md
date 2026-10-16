# Plantillas

## Registro (`remember`)

```json
{
  "items": [
    {
      "title": "<3-8 palabras, buscable>",
      "content": "<1-2 frases autosuficientes, con el porqué si lo hay>",
      "kind": "<decision|preference|fact|event|note>",
      "topics": ["<tema-1>", "<tema-2 opcional>"],
      "key": "<tema/aspecto, solo si es un dato que cambia>",
      "occurred_at": "<AAAA-MM-DD, la fecha real del hecho si no es hoy>",
      "relate": [{"id": 123, "relation": "<related|part_of|contradicts>"}]
    }
  ]
}
```

## Decisiones sobre las líneas `revisa:` (`revise` con `ops`)

```json
{
  "ops": [
    {"id": <nuevo>, "action": "relate", "with_id": <candidato>, "relation": "related"},
    {"id": <nuevo>, "action": "relate", "with_id": <candidato>, "relation": "contradicts"},
    {"id": <falso>, "action": "supersede", "with_id": <bueno>}
  ]
}
```

## Respuesta al usuario

```text
<todo lo pertinente que haya en lodan, una línea o fila por dato, incluido lo guardado con otro nombre>
<sin párrafos, sin relleno y sin ofrecer «¿quieres…?» datos que ya tienes>
```

## Línea al usuario tras guardar

```text
Guardado en lodan: <título> (<tipo>).
Relacionado en lodan: <título> con <título>.
```

## Pregunta ante una contradicción

```text
Tenía guardado <dato antiguo> (<fecha>); ahora dices <dato nuevo>. ¿Cuál es el bueno?
```

## Resumen de sesión (`session`, `action: end`)

```text
<Objetivo de la conversación>. Decidido: <decisiones clave>. Pendiente: <lo que queda, si hay>.
```
