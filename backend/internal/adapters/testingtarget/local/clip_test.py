import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

path = Path(__file__).resolve().parents[3] / "skillassets/testing/scripts/export_clip.py"
spec = importlib.util.spec_from_file_location("export_clip", path)
clip = importlib.util.module_from_spec(spec)
spec.loader.exec_module(clip)


class ClipSelectionTest(unittest.TestCase):
    def test_bounds_and_foreign_recording_are_refused_before_export(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            attempt = root / "attempt"
            attempt.mkdir()
            movie = attempt / "original.mov"
            movie.write_bytes(b"original")
            foreign = root / "foreign.mov"
            foreign.write_bytes(b"foreign")
            with patch.object(clip.subprocess, "run") as command:
                for start, seconds in [(-1, 1), (0, 16), (0, 0), (float("nan"), 1)]:
                    with self.assertRaises(ValueError):
                        clip.export(attempt, movie, start, seconds, "before")
                with self.assertRaises(ValueError):
                    clip.export(attempt, foreign, 0, 1, "before")
                command.assert_not_called()

    def test_selected_clip_retains_original_and_records_hashes(self):
        with tempfile.TemporaryDirectory() as tmp:
            attempt = Path(tmp)
            movie = attempt / "original.mov"
            movie.write_bytes(b"retained original")

            def command(args, **kwargs):
                if "-movflags" in args:
                    Path(args[-1]).write_bytes(b"selected clip")
                    self.assertIn("-n", args)

            with patch.object(clip, "duration", side_effect=[30, 9]):
                with patch.object(clip.subprocess, "run", side_effect=command):
                    result = clip.export(attempt, movie, 3, 9, "before")
            self.assertEqual(movie.read_bytes(), b"retained original")
            selection = json.loads(result.with_suffix(".selection.json").read_text())
            self.assertEqual(selection["startSeconds"], 3)
            self.assertEqual(selection["durationSeconds"], 9)
            self.assertEqual(selection["exportSHA256"], clip.digest(result))
            self.assertIn(clip.digest(result), result.with_suffix(".sha256").read_text())
