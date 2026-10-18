#!/usr/bin/env python3
"""Black-box tests for validar_registro.py (spec S7, helper script).

Run: python test_validar_registro.py   (exit code 0 = pass)
Only the standard library is used; the CLI is exercised through subprocess.
"""
import json
import os
import subprocess
import sys
import tempfile
import unittest

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "validar_registro.py")

ERR_RANGE = "items debe ser una lista de 1 a 200 registros"
ERR_RELATE = "registro {n}: relate debe ser una lista de {{id, relation}} con id entero positivo"
ERR_RELATION = "registro {n}: relation debe ser related, contradicts o part_of"
WARN_REL = "registro {n}: fecha relativa («{w}»); conviértela a AAAA-MM-DD"


def item(**over):
    base = {
        "title": "Desayuno de churros",
        "content": "El 2026-10-15 desayuné churros",
        "kind": "event",
        "topics": ["desayuno"],
    }
    base.update(over)
    return base


def run(payload, raw=None):
    """Run the CLI with payload written as UTF-8 to a temp file. Returns (code, parsed_json)."""
    fd, path = tempfile.mkstemp(suffix=".json")
    try:
        with os.fdopen(fd, "wb") as f:
            f.write((raw if raw is not None else json.dumps(payload, ensure_ascii=False)).encode("utf-8"))
        env = dict(os.environ, PYTHONIOENCODING="utf-8")
        p = subprocess.run(
            [sys.executable, SCRIPT, "--file", path],
            capture_output=True, env=env, timeout=60,
        )
    finally:
        os.remove(path)
    out = p.stdout.decode("utf-8")
    return p.returncode, json.loads(out)


class Limits(unittest.TestCase):  # S7 (a)
    def test_200_items_valid(self):
        code, out = run([item() for _ in range(200)])
        self.assertEqual(code, 0, out)
        self.assertTrue(out["valido"])
        self.assertEqual(out["errores"], [])

    def test_201_items_error(self):
        code, out = run([item() for _ in range(201)])
        self.assertEqual(code, 1)
        self.assertFalse(out["valido"])
        self.assertIn(ERR_RANGE, out["errores"])

    def test_zero_items_error(self):
        code, out = run([])
        self.assertEqual(code, 1)
        self.assertFalse(out["valido"])
        self.assertIn(ERR_RANGE, out["errores"])

    def test_wrapped_items_object_201_error(self):
        code, out = run({"items": [item() for _ in range(201)]})
        self.assertEqual(code, 1)
        self.assertIn(ERR_RANGE, out["errores"])


class Relate(unittest.TestCase):  # S7 (b)
    def test_valid_relate_with_and_without_relation(self):
        code, out = run([item(relate=[{"id": 5, "relation": "contradicts"}, {"id": 7}])])
        self.assertEqual(code, 0, out)
        self.assertTrue(out["valido"])
        self.assertEqual(out["errores"], [])

    def test_all_allowed_relations_valid(self):
        rel = [{"id": i + 1, "relation": r} for i, r in enumerate(["related", "contradicts", "part_of"])]
        code, out = run([item(relate=rel)])
        self.assertEqual(code, 0, out)

    def test_bad_ids(self):
        for bad in (0, -3, "4"):
            with self.subTest(id=bad):
                code, out = run([item(), item(relate=[{"id": bad}])])
                self.assertEqual(code, 1)
                self.assertFalse(out["valido"])
                self.assertIn(ERR_RELATE.format(n=2), out["errores"])

    def test_relate_not_a_list(self):
        for bad in ({"id": 1}, "1", 5):
            with self.subTest(relate=bad):
                code, out = run([item(relate=bad)])
                self.assertEqual(code, 1)
                self.assertIn(ERR_RELATE.format(n=1), out["errores"])

    def test_bad_relation_value(self):
        code, out = run([item(relate=[{"id": 3, "relation": "friend"}])])
        self.assertEqual(code, 1)
        self.assertFalse(out["valido"])
        self.assertIn(ERR_RELATION.format(n=1), out["errores"])


class RelativeDates(unittest.TestCase):  # S7 (c)
    def check_warns(self, text, word):
        code, out = run([item(content=text)])
        self.assertEqual(code, 0, out)  # warning, not error
        self.assertTrue(out["valido"])
        self.assertEqual(out["errores"], [])
        self.assertIn(WARN_REL.format(n=1, w=word), out["avisos"])

    def test_words(self):
        for text, word in (
            ("Ayer desayuné churros".lower(), "ayer"),
            ("hoy desayuné churros", "hoy"),
            ("Comimos paella la semana pasada", "la semana pasada"),
            ("Quedamos el martes en casa", "el martes"),
        ):
            with self.subTest(word=word):
                self.check_warns(text, word)

    def test_case_insensitive(self):
        for text, word in (("AYER desayuné churros", "ayer"), ("Hoy desayuné churros", "hoy"),
                           ("Fue LA SEMANA PASADA", "la semana pasada")):
            with self.subTest(text=text):
                code, out = run([item(content=text)])
                self.assertEqual(code, 0, out)
                got = [a.lower() for a in out["avisos"]]
                self.assertIn(WARN_REL.format(n=1, w=word).lower(), got)

    def test_warning_numbered_by_record(self):
        code, out = run([item(), item(content="ayer fui al médico")])
        self.assertEqual(code, 0, out)
        self.assertIn(WARN_REL.format(n=2, w="ayer"), out["avisos"])
        self.assertFalse([a for a in out["avisos"] if a.startswith("registro 1: fecha relativa")])

    def test_absolute_date_no_relative_warning(self):
        code, out = run([item(content="El 2026-10-15 desayuné churros")])
        self.assertEqual(code, 0, out)
        self.assertEqual([a for a in out["avisos"] if "fecha relativa" in a], [])


class ExistingBehavior(unittest.TestCase):  # unchanged behavior
    def test_valid_exit_0_and_json_shape(self):
        code, out = run([item()])
        self.assertEqual(code, 0)
        self.assertEqual(set(out), {"valido", "errores", "avisos"})
        self.assertTrue(out["valido"])
        self.assertEqual(out["errores"], [])
        self.assertEqual(out["avisos"], [])

    def test_wrapped_items_object_valid(self):
        code, out = run({"items": [item()]})
        self.assertEqual(code, 0, out)

    def test_invalid_kind_still_error_exit_1(self):
        code, out = run([item(kind="banana")])
        self.assertEqual(code, 1)
        self.assertFalse(out["valido"])
        self.assertTrue(any(e.startswith("registro 1: kind debe ser uno de") for e in out["errores"]), out)

    def test_secret_warning_still_present(self):
        code, out = run([item(content="Mi contraseña es 1234")])
        self.assertEqual(code, 0, out)
        self.assertIn("registro 1: parece contener un secreto; no se deben guardar secretos", out["avisos"])

    def test_unreadable_input_exit_2(self):
        code, out = run(None, raw="{not json")
        self.assertEqual(code, 2)
        self.assertFalse(out["valido"])
        self.assertTrue(out["errores"][0].startswith("entrada ilegible"))


if __name__ == "__main__":
    unittest.main()
