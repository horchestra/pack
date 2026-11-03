"""The YAML of docs/ is read here the way the Go sdk reads it (sdk/docs): the same documents, the same refusals."""

import sys
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))

from packsdk import SchemaError
from packsdk._docs import _examples, parse


class Subset(unittest.TestCase):
    def test_the_subset(self):
        src = (
            "# a comment\nbase: true\n/a/b:\n  title: Plain text # a comment\n  description: |\n    Two lines,\n\n"
            "    and a blank one between.\n\n  examples:\n    - \"quoted: with a colon\"\n    - 'it''s'\n    - 5432\n"
            "\"/q~1k\":\n  title: \"\\u00e9\\t\\\"x\\\"\"\ngroups:\n  g: G\n")
        m = parse(src)
        self.assertEqual(["base", "/a/b", "/q~1k", "groups"], list(m))
        self.assertEqual("Plain text", m["/a/b"]["title"])
        self.assertEqual("Two lines,\n\nand a blank one between.", m["/a/b"]["description"])
        self.assertEqual(["quoted: with a colon", "it's", "5432"], m["/a/b"]["examples"])
        self.assertEqual("é\t\"x\"", m["/q~1k"]["title"])

    def test_refusals(self):
        for name, src in {
            "flow map": "a: {b: c}\n",
            "flow list": "a: [b]\n",
            "anchor": "a: &x b\n",
            "folded": "a: >\n  b\n",
            "colon in plain": "a: b: c\n",
            "duplicate": "a: b\na: c\n",
            "tab": "a:\n\tb: c\n",
            "no value": "a:\n",
            "deeper sibling": "a: b\n  c: d\n",
            "list of maps": "a:\n  - b: c\n",
            "unclosed quote": "a: \"b\n",
            "text after quote": "a: \"b\" c\n",
            "bad escape": "a: \"\\q\"\n",
            "list at top": "- a\n",
        }.items():
            with self.subTest(name), self.assertRaises(SchemaError):
                parse(src)


class Examples(unittest.TestCase):
    """Typed by the field, written back as the file says, or refused — as in the Go sdk (sdk/docs)."""

    def test_numbers(self):
        import json
        node = {"type": "number"}
        for ok in ["0", "-3", "1.5", "0.25", "-0.0001", "123456789012.345", "9223372036854775807"]:
            self.assertEqual(ok, json.dumps(_examples(node, [ok])[0]), ok)
        for bad in ["nan", "inf", "-inf", "1e3", "1e999", "1.50", "1.0", ".5", "01.5", "0.00001", "1234567890123.456",
                    "9223372036854775808", "+1"]:
            with self.subTest(bad), self.assertRaises(SchemaError):
                _examples(node, [bad])

    def test_types(self):
        self.assertEqual([8080], _examples({"type": "integer"}, ["8080"]))
        self.assertEqual([True, False], _examples({"type": "boolean"}, ["true", "false"]))
        self.assertEqual(["8080"], _examples({"type": "string"}, ["8080"]))
        for node, x in [({"type": "integer"}, "80.5"), ({"type": "boolean"}, "yes"), ({"type": "object"}, "x"),
                        ({"type": "string", "format": "password"}, "x"), ({"$ref": "pack://p#"}, "x")]:
            with self.subTest(node), self.assertRaises(SchemaError):
                _examples(node, [x])


if __name__ == "__main__":
    unittest.main()
