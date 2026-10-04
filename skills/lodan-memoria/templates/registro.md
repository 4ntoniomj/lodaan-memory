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
      "occurred_at": "<AAAA-MM-DD, solo para eventos que no son de hoy>"
    }
  ]
}
```

## Línea al usuario tras guardar

```text
Guardado en lodan: <título> (<tipo>).
```

## Resumen de sesión (`session`, `action: end`)

```text
<Objetivo de la conversación>. Decidido: <decisiones clave>. Pendiente: <lo que queda, si hay>.
```
