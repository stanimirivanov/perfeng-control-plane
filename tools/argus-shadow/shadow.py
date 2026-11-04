"""Run a one-group Argus CI shadow pilot and retain correlated evidence."""

import argparse
import hashlib
import hmac
import json
import os
import re
import secrets
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path

SUITE = "control-plane-api"
ADAPTER = "generic-http"
MANIFEST = "argus.dev/execution-manifest/v1"
SHA = re.compile(r"[a-f0-9]{40}")
DIGEST = re.compile(r"[a-f0-9]{64}")
OUTCOMES = {"passed", "failed", "error", "skipped"}
ROOT = Path(__file__).resolve().parents[2]
LIMIT = 1 << 20


def revision(digest):
    return {"algorithm": "git-sha1", "digest": digest}


def read(path):
    with path.open("rb") as stream:
        raw = stream.read(LIMIT + 1)
    if len(raw) > LIMIT:
        raise ValueError("shadow input exceeds bound")
    return json.loads(raw)


def write(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def selection(document, repo, base, head, number, delivery, tests):
    try:
        change = document["change"]
        catalog = document["catalog"]
        if (
            document["apiVersion"] != MANIFEST
            or document["family"] != "functional-api"
            or change["sourceRepository"] != repo
            or change["baseRevision"] != revision(base)
            or change["headRevision"] != revision(head)
            or change["pullRequest"] != {"number": number}
            or change["trigger"]["deliveryId"] != delivery
            or catalog["sourceRepository"] != repo
            or catalog["revision"] != revision(base)
        ):
            return None
        seen, selected = set(), set()
        for entry in document["decisions"]:
            key = entry["test"]["key"]
            if (
                entry["testRepository"] != repo
                or entry["suite"] != {"key": SUITE, "adapter": ADAPTER, "family": "functional-api"}
                or key not in tests or key in seen
                or entry["outcome"] not in {"RUN_REQUIRED", "SKIP_FOR_NOW"}
            ):
                return None
            seen.add(key)
            if entry["outcome"] == "RUN_REQUIRED":
                selected.add(key)
        return selected if seen == tests else None
    except (TypeError, KeyError):
        return None


def correlated(attempt, stage, attempt_id, repo, head, digest, expected):
    try:
        rows = attempt["results"]
        return (
            attempt["apiVersion"] == "argus.dev/execution-attempt/v1"
            and attempt["stage"] == stage
            and attempt["attemptId"] == attempt_id
            and attempt["testRepository"] == repo
            and attempt["testRevision"] == revision(head)
            and attempt["manifest"]["sha256"] == digest
            and attempt["adapter"] == {"id": ADAPTER, "version": "1.0.0"}
            and len(rows) == len(expected)
            and {row["testKey"] for row in rows} == expected
            and all(row["suiteKey"] == SUITE and row["outcome"] in OUTCOMES for row in rows)
        )
    except (TypeError, KeyError):
        return False


def command(args, *, cwd=None, env=None, data=None, timeout=120):
    return subprocess.run(
        args, cwd=cwd, env=env, input=data, capture_output=True, check=False, timeout=timeout,
    )


def succeeded(args, **kwargs):
    result = command(args, **kwargs)
    if result.returncode or len(result.stdout) > LIMIT:
        raise ValueError("shadow command did not produce bounded evidence")
    return result.stdout


def inputs(event_path):
    event = read(event_path)
    pull = event["pull_request"]
    base, head = pull["base"]["sha"], pull["head"]["sha"]
    repo = read(ROOT / ".argus/test-catalog.json")["repository"]
    name = repo["owner"] + "/" + repo["name"]
    if (
        event["action"] != "closed" or pull["merged"] is not True
        or pull["base"]["ref"] != "main"
        or any(pull[side]["repo"]["full_name"] != name for side in ("base", "head"))
        or str(event["repository"]["id"]) != repo["providerRepositoryId"]
        or not SHA.fullmatch(base) or not SHA.fullmatch(head)
        or type(event["number"]) is not int or event["number"] <= 0
    ):
        raise ValueError("event is outside the reviewed pilot")
    return event, repo, base, head


def produce(args):
    event, _, base, _ = inputs(args.event)
    catalog_path = args.private / "base-catalog.json"
    catalog_path.write_bytes(succeeded(["git", "show", f"{base}:.argus/test-catalog.json"], cwd=ROOT))
    # Replay the reviewed merged PR comparison into an isolated Argus instance.
    event["action"] = "synchronize"
    body = json.dumps(event).encode()
    secret = secrets.token_hex(32)
    env = {
        **os.environ,
        "ARGUS_HTTP_ADDRESS": "127.0.0.1:18080",
        "ARGUS_GITHUB_WEBHOOK_SECRET": secret,
    }
    succeeded([args.bin / "migrate"], env=env)
    succeeded([args.bin / "catalog", "import", "-revision", base, catalog_path], env=env)
    with subprocess.Popen(
        [args.bin / "control-plane"], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    ) as server:
        try:
            request = urllib.request.Request(
                "http://127.0.0.1:18080/webhooks/github", data=body,
                headers={"Content-Type": "application/json", "X-GitHub-Event": "pull_request",
                         "X-GitHub-Delivery": args.delivery,
                         "X-Hub-Signature-256": "sha256=" + hmac.digest(secret.encode(), body, "sha256").hex()},
            )
            for _ in range(20):
                try:
                    with urllib.request.urlopen(request, timeout=25) as response:
                        if response.status not in (200, 201):
                            raise ValueError("Argus did not accept the comparison")
                    break
                except urllib.error.HTTPError:
                    raise
                except urllib.error.URLError:
                    if server.poll() is not None:
                        raise ValueError("Argus exited")
                    time.sleep(0.5)
            else:
                raise ValueError("Argus ingress unavailable")
            raw = succeeded([args.bin / "select", "-delivery-id", args.delivery], env=env)
            (args.private / "manifest.json").write_bytes(raw)
        finally:
            server.terminate()
            try:
                server.wait(timeout=8)
            except subprocess.TimeoutExpired:
                server.kill()


def measured(run):
    start = time.monotonic()
    result = run()
    return result, round((time.monotonic() - start) * 1000)


def normalized(args, stage, repo, head, digest, expected):
    attempt_id = f"{args.delivery}-{stage}"
    result, elapsed = measured(lambda: command([
        args.bin / "run-functional-api", "-manifest", args.private / "manifest.json",
        "-stage", stage, "-attempt-id", attempt_id, "-test-provider", "github",
        "-test-host", "github.com", "-test-repository-id", repo["providerRepositoryId"],
        "-test-revision", head, "-adapter", ADAPTER, "-timeout", "3m",
        "--", args.bin / "adapter",
    ], cwd=args.subject, timeout=200))
    if len(result.stdout) > LIMIT:
        raise ValueError("attempt exceeds evidence limit")
    attempt = json.loads(result.stdout)
    if not correlated(attempt, stage, attempt_id, repo, head, digest, expected):
        raise ValueError("attempt does not match the approved pilot")
    write(args.output / f"{stage}.json", attempt)
    return {"status": "observed", "elapsedMs": elapsed, "exitCode": result.returncode}


def fallback(args, repo, head, tests):
    attempt_id = args.delivery + "-fallback"
    payload = {
        "apiVersion": "argus.dev/functional-api-adapter-request/v1",
        "attemptId": attempt_id,
        "manifest": {"apiVersion": MANIFEST, "sha256": hashlib.sha256(b"control-plane-pilot-fallback/v1").hexdigest()},
        "stage": "full-suite", "testRepository": repo, "testRevision": revision(head), "adapter": ADAPTER,
        "tests": [{"suiteKey": SUITE, "testKey": test["key"], "name": test["name"]} for test in tests],
    }
    raw, elapsed = measured(lambda: succeeded([args.bin / "adapter"], cwd=args.subject,
                                               data=json.dumps(payload).encode(), timeout=200))
    result = json.loads(raw)
    expected = {test["key"] for test in tests}
    rows = result["results"]
    if (
        result["apiVersion"] != "argus.dev/functional-api-adapter-result/v1"
        or result["attemptId"] != attempt_id
        or result["adapter"] != {"id": ADAPTER, "version": "1.0.0"}
        or len(rows) != len(expected) or {row["testKey"] for row in rows} != expected
        or any(row["suiteKey"] != SUITE or row["outcome"] not in OUTCOMES for row in rows)
    ):
        raise ValueError("fallback result is incomplete or foreign")
    write(args.output / "full-fallback.json", result)
    return {"status": "fallback", "elapsedMs": elapsed}


def observe(args):
    event, repo, base, head = inputs(args.event)
    actual = succeeded(["git", "rev-parse", "HEAD"], cwd=args.subject).decode().strip()
    if actual != head:
        raise ValueError("test checkout does not match the reviewed head")
    [suite] = read(ROOT / ".argus/test-catalog.json")["testSuites"]
    tests = suite["tests"]
    keys = {test["key"] for test in tests}
    config = read(ROOT / ".argus/functional-api-execution-bindings.json")
    [binding] = config["groups"]
    if binding["testRepository"] != repo or binding["adapter"] != ADAPTER:
        raise ValueError("pilot binding does not match the repository")
    if config["apiVersion"] != "argus.dev/functional-api-execution-bindings/v1" or binding["groupKey"] != SUITE:
        raise ValueError("pilot binding identity is invalid")
    binding["testRevision"] = revision(head)
    write(args.private / "bindings.json", config)
    manifest_path = args.private / "manifest.json"
    summary = {"schemaVersion": 1, "pullRequest": event["number"], "deliveryId": args.delivery,
               "baseRevision": base, "testRevision": head, "selected": {"status": "missing"},
               "full": {"status": "missing"}}
    try:
        try:
            manifest = read(manifest_path)
            selected = selection(manifest, repo, base, head, event["number"], args.delivery, keys)
            if selected is None:
                raise ValueError("manifest is stale or incomplete")
            plan = json.loads(succeeded([args.bin / "plan-functional-api", "-manifest", manifest_path,
                                         "-bindings", args.private / "bindings.json"]))
            digest = plan["manifest"]["sha256"]
            if not DIGEST.fullmatch(digest):
                raise ValueError("invalid manifest digest")
            summary["manifestSha256"] = digest
            write(args.output / "manifest.json", manifest)
            write(args.output / "plan.json", plan)
        except (OSError, ValueError, TypeError, KeyError, subprocess.TimeoutExpired):
            summary["full"] = fallback(args, repo, head, tests)
            return
        if selected:
            try:
                summary["selected"] = normalized(args, "selected", repo, head, digest, selected)
            except (OSError, ValueError, TypeError, KeyError, subprocess.TimeoutExpired):
                summary["selected"] = {"status": "invalid"}
        else:
            summary["selected"] = {"status": "empty"}
        try:
            summary["full"] = normalized(args, "full-suite", repo, head, digest, keys)
        except (OSError, ValueError, TypeError, KeyError, subprocess.TimeoutExpired):
            summary["full"] = fallback(args, repo, head, tests)
    finally:
        write(args.output / "observation.json", summary)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("select", "observe"))
    for name in ("event", "bin", "private", "subject", "output"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--delivery", required=True)
    args = parser.parse_args()
    try:
        (produce if args.mode == "select" else observe)(args)
    except (OSError, ValueError, TypeError, KeyError, subprocess.TimeoutExpired):
        raise SystemExit("Argus shadow stage could not complete.") from None


if __name__ == "__main__":
    main()
