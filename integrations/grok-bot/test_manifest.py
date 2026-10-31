"""Run with python3 integrations/grok-bot/test_manifest.py."""

import contextlib
import hashlib
import io
import json
import runpy
import unittest
from pathlib import Path
from unittest.mock import patch


class ManifestTests(unittest.TestCase):
    def test_files_close_on_success_and_read_or_write_failure(self):
        script = Path(__file__).with_name("make-manifest.py")
        for failure in (None, "read", "write"):
            with self.subTest(failure=failure):
                skill_file = io.BytesIO(b"skill content")
                manifest_file = io.StringIO()
                written = []
                real_dump = json.dump

                def dump(value, handle, **kwargs):
                    if failure == "write":
                        raise OSError("write failed")
                    written.append(value)
                    real_dump(value, handle, **kwargs)

                with (
                    patch("sys.argv", [str(script), "test-version"]),
                    patch("os.listdir", return_value=["example"]),
                    patch("builtins.open", side_effect=[skill_file, manifest_file]),
                    patch("json.dump", side_effect=dump),
                    patch.object(skill_file, "read", side_effect=OSError("read failed"))
                    if failure == "read"
                    else contextlib.nullcontext(),
                    contextlib.redirect_stdout(io.StringIO()),
                ):
                    if failure:
                        with self.assertRaisesRegex(OSError, f"{failure} failed"):
                            runpy.run_path(str(script), run_name="__main__")
                    else:
                        runpy.run_path(str(script), run_name="__main__")
                self.assertTrue(skill_file.closed)
                if failure != "read":
                    self.assertTrue(manifest_file.closed)
                if failure is None:
                    self.assertEqual(
                        written,
                        [
                            {
                                "name": "ao-grok-bot",
                                "version": "test-version",
                                "skills": [
                                    {
                                        "id": "example",
                                        "path": "skills/example/SKILL.md",
                                        "bytes": 13,
                                        "sha256": hashlib.sha256(
                                            b"skill content"
                                        ).hexdigest(),
                                    }
                                ],
                            }
                        ],
                    )


if __name__ == "__main__":
    unittest.main()
