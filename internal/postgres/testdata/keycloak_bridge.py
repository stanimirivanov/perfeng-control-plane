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
        state_file.write_text(json.dumps({"nonce": label.split("=", 1)[1], "root": mount.removesuffix(":/fixture:ro")}))
        state_file.chmod(0o600)
        state["rootPEM"] = (Path(mount.removesuffix(":/fixture:ro")) / "tls.pem").read_text()
    return original_docker(*args)


def observe_verified(token, jwks, issuer, **profile):
    result = original_verify(token, jwks, issuer, **profile)
    state["issuer"] = issuer
    client = profile.get("client", fixture.CI_CLIENT)
    if profile.get("kind") == "ID":
        name = "humanID"
    else:
        name = {fixture.CI_CLIENT: "ci", fixture.human.LOGIN: "initial", fixture.human.DELEGATE: "delegated"}[client]
    tokens[name] = token
    if len(tokens) == 4 and not state.get("conformance"):
        # Pause before destructive replay/revocation cases, with a live issuer.
        sys.stdout.write(json.dumps({"issuer": issuer, "rootPEM": state["rootPEM"], **tokens}) + "\n")
        sys.stdout.flush()
        if sys.stdin.readline() != "VERIFIED\n":
            raise ValueError("Control-plane qualification refused")
        state["conformance"] = True
    return result


def qualify_receipt(checks):
    result = original_receipt(checks)
    if not state.get("conformance"):
        raise ValueError("Control-plane qualification missing")
    result["passedChecks"].append("control-plane-real-token-conformance")
    result["remainingGates"].remove("control-plane-real-token-conformance")
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
