from pathlib import Path
from tempfile import TemporaryDirectory
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import shadow

REPO = {"provider":"github","host":"github.com","providerRepositoryId":"1354503944","owner":"stanimirivanov","name":"perfeng-control-plane"}
BASE, HEAD = "a" * 40, "b" * 40
TESTS = {"run-lifecycle", "run-authentication"}


def manifest():
    return {
        "apiVersion": "argus.dev/execution-manifest/v1",
        "family": "functional-api",
        "change": {"sourceRepository": REPO, "baseRevision": shadow.revision(BASE),
                   "headRevision": shadow.revision(HEAD), "pullRequest": {"number": 8},
                   "trigger": {"deliveryId": "pilot-1"}},
        "catalog": {"sourceRepository": REPO, "revision": shadow.revision(BASE)},
        "decisions": [
            {"testRepository": REPO, "suite": {"key": "control-plane-api", "adapter": "generic-http", "family": "functional-api"},
             "test": {"key": key}, "outcome": "RUN_REQUIRED" if key == "run-lifecycle" else "SKIP_FOR_NOW"}
            for key in sorted(TESTS)
        ],
    }


class ShadowTests(unittest.TestCase):
    def test_stale_or_incomplete_selection_requires_full_fallback(self):
        stale = manifest()
        stale["change"]["headRevision"] = shadow.revision(BASE)
        incomplete = manifest()
        incomplete["decisions"].pop()
        duplicate = manifest()
        duplicate["decisions"].append(duplicate["decisions"][0])
        for document in (None, stale, incomplete, duplicate):
            with self.subTest(document=document):
                self.assertIsNone(shadow.selection(document, REPO, BASE, HEAD, 8, "pilot-1", TESTS))

    def test_selects_exact_subset_and_retains_control(self):
        self.assertEqual(
            shadow.selection(manifest(), REPO, BASE, HEAD, 8, "pilot-1", TESTS), {"run-lifecycle"}
        )

    def test_attempt_refuses_foreign_digest_and_incomplete_results(self):
        attempt = {"apiVersion":"argus.dev/execution-attempt/v1","stage":"full-suite","attemptId":"pilot-1-full-suite",
                   "testRevision":shadow.revision(HEAD),"testRepository":REPO,"manifest":{"sha256":"f"*64},
                   "adapter":{"id":"generic-http","version":"1.0.0"},
                   "results":[{"suiteKey":"control-plane-api","testKey":key,"outcome":"passed"} for key in TESTS]}
        self.assertTrue(shadow.correlated(attempt, "full-suite", "pilot-1-full-suite", REPO, HEAD, "f"*64, TESTS))
        self.assertFalse(shadow.correlated(attempt, "full-suite", "pilot-1-full-suite", REPO, HEAD, "e"*64, TESTS))
        attempt["results"].pop()
        self.assertFalse(shadow.correlated(attempt, "full-suite", "pilot-1-full-suite", REPO, HEAD, "f"*64, TESTS))

    def test_observe_runs_full_fallback_with_no_manifest(self):
        with TemporaryDirectory() as directory:
            path = Path(directory)
            args = SimpleNamespace(event=None, subject=path, private=path, output=path, delivery="pilot-1")
            with (
                patch.object(shadow, "inputs", return_value=({"number": 8}, REPO, BASE, HEAD)),
                patch.object(shadow, "succeeded", return_value=HEAD.encode()),
                patch.object(shadow, "fallback", return_value={"status": "fallback"}) as fallback,
                patch.object(shadow, "normalized") as normalized,
            ):
                shadow.observe(args)
            fallback.assert_called_once()
            normalized.assert_not_called()
            observation = shadow.read(path / "observation.json")
            self.assertEqual(observation["selected"]["status"], "missing")
            self.assertEqual(observation["full"]["status"], "fallback")

    def test_invalid_selected_attempt_still_runs_normalized_control(self):
        with TemporaryDirectory() as directory:
            path = Path(directory)
            args = SimpleNamespace(event=None, subject=path, private=path, output=path, bin=path, delivery="pilot-1")
            shadow.write(path / "manifest.json", manifest())
            with (
                patch.object(shadow, "inputs", return_value=({"number": 8}, REPO, BASE, HEAD)),
                patch.object(shadow, "succeeded", side_effect=[HEAD.encode(), b'{"manifest":{"sha256":"' + b'f' * 64 + b'"}}']),
                patch.object(shadow, "selection", return_value={"run-lifecycle"}),
                patch.object(shadow, "fallback") as fallback,
                patch.object(shadow, "normalized", side_effect=[ValueError(), {"status": "observed"}]) as normalized,
            ):
                shadow.observe(args)
            self.assertEqual([call.args[1] for call in normalized.call_args_list], ["selected", "full-suite"])
            fallback.assert_not_called()
            observation = shadow.read(path / "observation.json")
            self.assertEqual(observation["selected"]["status"], "invalid")
            self.assertEqual(observation["full"]["status"], "observed")
