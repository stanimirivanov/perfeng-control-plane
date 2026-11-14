"""Private test pipe adapter; never log, persist or export credentials."""
import json
import signal
import sys
from pathlib import Path

from scripts import keycloak_qualification as fixture

state_file = Path(sys.argv[1])
state = {}
tokens = {}
original_docker = fixture.docker
original_verify = fixture.verify
original_receipt = fixture.receipt


def observe_docker(*args):
    if args[0] == "create":
        label = args[args.index("--label") + 1]
        mount = next(arg for arg in args if arg.endswith(":/fixture:ro"))
        state["nonce"] = label.split("=", 1)[1]
        state_file.write_text(json.dumps({"nonce": state["nonce"], "root": mount.removesuffix(":/fixture:ro")}))
        state_file.chmod(0o600)
        state["rootPEM"] = (Path(mount.removesuffix(":/fixture:ro")) / "tls.pem").read_text()
    result = original_docker(*args)
    if args[0] == "create":
        state["container"] = result
    return result


def observe_verified(token, jwks, issuer, **profile):
    result = original_verify(token, jwks, issuer, **profile)
    state["issuer"] = issuer
    client = profile.get("client", fixture.CI_CLIENT)
    if profile.get("kind") == "ID":
        name = "humanID"
    else:
        names = {fixture.CI_CLIENT: "ci", fixture.human.LOGIN: "initial", fixture.human.DELEGATE: "delegated"}
        if client not in names:
            return result  # Fixture administrator is checked by the original verifier only.
        name = names[client]
    tokens[name] = token
    if len(tokens) == 4 and not state.get("conformance"):
        state["oldKid"] = key_id(tokens["ci"])
        sys.stdout.write(json.dumps({"issuer": issuer, "rootPEM": state["rootPEM"], **tokens}) + "\n")
        sys.stdout.flush()
        if sys.stdin.readline() != "IDENTITY_AND_GRANTS_VERIFIED\n":
            raise ValueError("Control-plane qualification refused")
        state["conformance"] = True
    elif state.get("conformance") and client == fixture.CI_CLIENT:
        current = key_id(token)
        published = {key["kid"] for key in jwks["keys"] if key.get("alg") == "RS256" and key.get("use") == "sig"}
        if current != state["oldKid"] and state["oldKid"] in published and not state.get("overlap"):
            checkpoint("overlap", "OVERLAP_VERIFIED", token)
            state["overlap"] = True
        elif current != state["oldKid"] and state["oldKid"] not in published and state.get("overlap") and not state.get("retired"):
            checkpoint("retired", "RETIREMENT_VERIFIED", token)
            state["retired"] = True
    return result


def key_id(token):
    return json.loads(fixture.decode(token.split(".")[0]))["kid"]


def checkpoint(phase, expected, token=None):
    sys.stdout.write(json.dumps({"phase": phase, **({"ci": token} if token else {})}) + "\n")
    sys.stdout.flush()
    if sys.stdin.readline() != expected + "\n":
        raise ValueError("Control-plane phase refused")


def qualify_receipt(checks):
    result = original_receipt(checks)
    if not all(state.get(name) for name in ("conformance", "overlap", "retired")):
        raise ValueError("Control-plane rotation qualification missing")
    checkpoint("outage-ready", "STOP_ISSUER", tokens["ci"])
    container = state["container"]
    if original_docker("inspect", "--format", '{{ index .Config.Labels "perfeng.keycloak.fixture" }}', container) != state["nonce"]:
        raise ValueError("Foreign issuer stop refused")
    original_docker("stop", "--time", "1", container)
    if original_docker("inspect", "--format", "{{.State.Running}}", container) != "false":
        raise ValueError("Issuer outage was not confirmed")
    checkpoint("stopped", "OUTAGE_VERIFIED")
    result["passedChecks"].extend(["control-plane-real-token-conformance", "control-plane-real-key-rotation", "control-plane-unavailable-key-freshness", "control-plane-named-http-boundary", "control-plane-current-service-grants"])
    result["remainingGates"].remove("control-plane-real-token-conformance")
    result["remainingGates"].remove("control-plane-real-key-rotation")
    return result


def stop(_signal, _frame):
    raise RuntimeError("Qualification cancelled")


signal.signal(signal.SIGTERM, stop)
fixture.docker = observe_docker
fixture.verify = observe_verified
fixture.receipt = qualify_receipt
try:
    # execute's finally cleans the fixture before the redacted receipt is emitted.
    result = fixture.execute()
    print(json.dumps(result), flush=True)
except BaseException:
    raise SystemExit(1) from None
