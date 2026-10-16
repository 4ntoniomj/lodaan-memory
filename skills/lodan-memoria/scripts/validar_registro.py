#!/usr/bin/env python3
"""Valida registros para la herramienta `remember` de lodan antes de enviarlos.

Comprueba los mismos límites que aplica el servidor (lodan v2) y avisa de
problemas de estilo que suelen empeorar la recuperación, como las fechas
relativas («ayer», «hoy»), que deben guardarse como AAAA-MM-DD.

Uso:
  validar_registro.py --json '<lista de items o {"items": [...]}>'
  validar_registro.py --file registros.json

Salida: JSON {"valido": bool, "errores": [...], "avisos": [...]}.
Códigos: 0 válido, 1 con errores, 2 entrada ilegible.
"""
import argparse
import json
import re
import sys
from datetime import datetime

KINDS = {"fact", "preference", "decision", "event", "note"}
RELACIONES = ("related", "contradicts", "part_of")
MAX_ITEMS = 200
_DIAS = "lunes|martes|miércoles|miercoles|jueves|viernes|sábado|sabado|domingo"
FECHA_RELATIVA = re.compile(
    r"(?i)(?<!\w)(pasado mañana|anteayer|ayer|hoy|mañana|la semana pasada|el mes pasado|"
    r"(?:este|el) (?:" + _DIAS + r"))(?!\w)"
)
SECRETO = re.compile(r"(?i)(contraseña|password|api[_ -]?key|token|secret|\bdni\b|\biban\b|\bES\d{22}\b)")


def validar(items):
    errores, avisos = [], []
    if not isinstance(items, list) or not 1 <= len(items) <= MAX_ITEMS:
        return [f"items debe ser una lista de 1 a {MAX_ITEMS} registros"], avisos
    for i, it in enumerate(items, 1):
        p = f"registro {i}"
        if not isinstance(it, dict):
            errores.append(f"{p}: no es un objeto")
            continue
        title = str(it.get("title", "")).strip()
        content = str(it.get("content", "")).strip()
        kind = it.get("kind")
        topics = it.get("topics") or []
        if not 1 <= len(title) <= 200:
            errores.append(f"{p}: title debe tener entre 1 y 200 caracteres")
        if not 1 <= len(content) <= 8000:
            errores.append(f"{p}: content debe tener entre 1 y 8000 caracteres")
        if kind not in KINDS:
            errores.append(f"{p}: kind debe ser uno de {sorted(KINDS)}")
        if not isinstance(topics, list):
            errores.append(f"{p}: topics debe ser una lista")
            topics = []
        if len(topics) > 3:
            avisos.append(f"{p}: más de 3 temas; usa de 1 a 3")
        if not topics:
            avisos.append(f"{p}: sin temas; el servidor usará «general»")
        if len(title.split()) > 10:
            avisos.append(f"{p}: título largo; mejor de 3 a 8 palabras")
        if kind == "event" and it.get("key"):
            avisos.append(f"{p}: los eventos no deberían llevar key")
        occ = it.get("occurred_at")
        if occ:
            try:
                datetime.fromisoformat(str(occ).replace("Z", "+00:00"))
            except ValueError:
                errores.append(f"{p}: occurred_at debe ser AAAA-MM-DD o RFC3339")
        errores.extend(validar_relate(p, it.get("relate")))
        vistas = set()
        for m in FECHA_RELATIVA.finditer(content):
            palabra = m.group(1).lower()
            if palabra not in vistas:
                vistas.add(palabra)
                avisos.append(f"{p}: fecha relativa («{palabra}»); conviértela a AAAA-MM-DD")
        if SECRETO.search(title + " " + content):
            avisos.append(f"{p}: parece contener un secreto; no se deben guardar secretos")
    return errores, avisos


def validar_relate(p, relate):
    """Errores de la lista relate de un registro (None si no la lleva)."""
    if relate is None:
        return []
    forma = f"{p}: relate debe ser una lista de {{id, relation}} con id entero positivo"
    if not isinstance(relate, list):
        return [forma]
    errores = []
    for r in relate:
        rid = r.get("id") if isinstance(r, dict) else None
        if not isinstance(rid, int) or isinstance(rid, bool) or rid <= 0:
            if forma not in errores:
                errores.append(forma)
            continue
        rel = r.get("relation")
        if rel is not None and rel not in RELACIONES:
            msg = f"{p}: relation debe ser related, contradicts o part_of"
            if msg not in errores:
                errores.append(msg)
    return errores


def main():
    ap = argparse.ArgumentParser(description="Valida registros para remember de lodan.")
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--json", help="JSON con la lista de items o {\"items\": [...]}")
    g.add_argument("--file", help="Ruta a un archivo JSON con el mismo formato")
    a = ap.parse_args()
    try:
        raw = a.json if a.json is not None else open(a.file, encoding="utf-8").read()
        data = json.loads(raw)
    except (OSError, json.JSONDecodeError) as e:
        print(json.dumps({"valido": False, "errores": [f"entrada ilegible: {e}"], "avisos": []}, ensure_ascii=False))
        return 2
    items = data.get("items") if isinstance(data, dict) else data
    errores, avisos = validar(items)
    print(json.dumps({"valido": not errores, "errores": errores, "avisos": avisos}, ensure_ascii=False, indent=2))
    return 1 if errores else 0


if __name__ == "__main__":
    sys.exit(main())
