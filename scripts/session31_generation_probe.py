#!/usr/bin/env python3
"""Apply scoped coordinator rules, or compare them with a recorded request.

--apply-rules updates only og-workflow's orchestratorRules through AO's API.
Replay mode records returned tool calls, never executes them.
Use --self-check without contacting AO or the provider.
"""

import argparse
import copy
import hashlib
import json
import time
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

RULES = """Finish the current coordination request without reopening unrelated work.
For a worker report, identify the change and the required decision or handoff.
Check the required evidence, perform the handoff, then finish the turn.
Reuse evidence for unchanged artifacts unless it expires or a required gate
requires a fresh check.
Do not repeat time reads, hashes, transcript scans, or draft checks without
new evidence or a required gate.
Do not add audit notes, review rounds, or checklists to routine status handling
unless requested or needed for safety.
Delegate detailed investigation and implementation to the responsible worker.
Do not duplicate that work here.
Do not keep a turn open while waiting for workers.
State the dependency and finish; handle their next report separately.
Use AO's delivered worker reports for coordination.
Do not create turn-end transcript watchers when those reports cover the same work.
Use a transcript scan only to resolve a specific missing or contradictory fact.
Treat task reminders as a state index, not a command to restart all pending work.
Preserve required approvals, safety checks, evidence requirements, routing,
model, and effort settings.
If evidence is missing, report the specific blocker.
Never invent evidence or label an incomplete task complete."""


def recorded_request(events, since):
    blobs, shapes, requests = {}, {}, {}
    for event in events:
        kind = event.get("type")
        if kind == "api-request-blob":
            blobs[event["hash"]] = event["message"]
        elif kind == "api-request-shape":
            shapes[event["shapeHash"]] = event["shape"]
        if kind != "api-request":
            continue
        base = requests[event["base"]] if event.get("base") else []
        keep = event.get("keep", 0)
        if not 0 <= keep <= len(base):
            raise ValueError("Recorded request has an invalid base length")
        refs = base[:keep] + event.get("tail", [])
        requests[event["id"]] = refs
        if event["timestamp"] < since:
            continue
        missing = set(refs) - blobs.keys()
        if missing:
            print(
                json.dumps(
                    {"unreplayable": event["id"], "missing_messages": len(missing)}
                ),
                flush=True,
            )
            continue
        request = copy.deepcopy(shapes[event["shapeHash"]])
        request.update(event["params"])
        request["messages"] = copy.deepcopy([blobs[ref] for ref in refs])
        restore_cache_control(request, event.get("cacheControl", []))
        return event["id"], request
    raise ValueError("No complete recorded request matches the requested time")


def restore_cache_control(request, markers):
    for marker in markers:
        target = request
        for key in marker["path"]:
            target = target[key]
        target["cache_control"] = marker["value"]


def candidate(request):
    result = copy.deepcopy(request)
    system = result.get("system", [])
    if isinstance(system, str):
        system = [{"type": "text", "text": system}]
    result["system"] = system + [{"type": "text", "text": RULES}]
    return result


def run_request(label, request, env):
    payload = copy.deepcopy(request)
    betas = payload.pop("betas", [])
    payload["stream"] = True
    headers = {
        "Content-Type": "application/json",
        "anthropic-version": "2023-06-01",
        "anthropic-beta": ",".join(betas),
        "Authorization": "Bearer " + env["ANTHROPIC_AUTH_TOKEN"],
    }
    req = urllib.request.Request(
        env["ANTHROPIC_BASE_URL"].rstrip("/") + "/v1/messages",
        data=json.dumps(payload).encode(),
        headers=headers,
    )
    started = time.monotonic()
    result = {"case": label, "usage": {}, "actions": [], "text": ""}
    ended = False
    with urllib.request.urlopen(req, timeout=600) as response:
        for raw in response:
            if not raw.startswith(b"data: "):
                continue
            event = json.loads(raw[6:])
            kind = event.get("type")
            if kind == "error":
                raise RuntimeError("Provider returned a stream error")
            if kind == "message_start":
                result["ttft_seconds"] = round(time.monotonic() - started, 3)
                message = event["message"]
                result["model"] = message["model"]
                result["usage"].update(message.get("usage", {}))
            elif (
                kind == "content_block_start"
                and event["content_block"]["type"] == "tool_use"
            ):
                result["actions"].append(event["content_block"]["name"])
            elif (
                kind == "content_block_delta" and event["delta"]["type"] == "text_delta"
            ):
                result["text"] += event["delta"]["text"]
            elif kind == "message_delta":
                result["usage"].update(event.get("usage", {}))
                result["stop_reason"] = event["delta"].get("stop_reason")
            elif kind == "message_stop":
                ended = True
    if not ended or result.get("stop_reason") not in {"end_turn", "tool_use"}:
        raise RuntimeError("Provider did not finish the model response")
    result["seconds"] = round(time.monotonic() - started, 3)
    return result


def rule_config(config):
    updated = copy.deepcopy(config)
    existing = updated.get("orchestratorRules", "")
    if RULES not in existing:
        updated["orchestratorRules"] = existing + ("\n\n" if existing else "") + RULES
    return updated


def apply_rules():
    url = "http://127.0.0.1:3001/api/v1/projects/og-workflow"
    with urllib.request.urlopen(url, timeout=15) as response:
        project = json.load(response)["project"]
    original = project["config"]
    updated = rule_config(original)
    request = urllib.request.Request(
        url + "/config",
        method="PUT",
        data=json.dumps(
            {"config": updated, "expectedRevision": project["revision"]}
        ).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=15) as response:
        result = json.load(response)["project"]
    if result["config"] != updated:
        raise RuntimeError("AO returned unexpected config; inspect before continuing")
    with urllib.request.urlopen(url, timeout=15) as response:
        persisted = json.load(response)["project"]
    if persisted["config"] != updated:
        raise RuntimeError("Persisted config differs; inspect before continuing")
    print(
        json.dumps(
            {
                "project": project["id"],
                "revision_before": project["revision"],
                "revision_after": persisted["revision"],
                "changed_fields": [
                    key for key in updated if original.get(key) != updated[key]
                ],
                "other_config_unchanged": True,
            }
        )
    )


def self_check():
    events = [
        {"type": "api-request-shape", "shapeHash": "s", "shape": {"system": []}},
        {"type": "api-request-blob", "hash": "a", "message": {"role": "user"}},
        {"type": "api-request-blob", "hash": "b", "message": {"role": "assistant"}},
        {
            "type": "api-request",
            "id": "first",
            "shapeHash": "s",
            "timestamp": "1",
            "tail": ["a"],
            "params": {"model": "test"},
        },
        {
            "type": "api-request",
            "id": "second",
            "shapeHash": "s",
            "base": "first",
            "timestamp": "2",
            "keep": 1,
            "tail": ["b"],
            "params": {"model": "test", "output_config": {"effort": "max"}},
        },
    ]
    identity, original = recorded_request(events, "2")
    assert identity == "second"
    assert original["messages"] == [{"role": "user"}, {"role": "assistant"}]
    revised = candidate(original)
    assert revised.pop("system") == [{"type": "text", "text": RULES}]
    assert revised == {key: value for key, value in original.items() if key != "system"}
    assert original["system"] == []
    events[-1]["keep"] = 2
    try:
        recorded_request(events, "2")
    except ValueError:
        pass
    else:
        raise AssertionError("Invalid request ancestry was accepted")
    config = {
        "orchestratorRules": "Preserve existing approval rules.",
        "env": {"ROUTE": "local"},
        "worker": {"agent": "claude-code"},
        "orchestrator": {"agentConfig": {"effort": "max"}},
    }
    updated = rule_config(config)
    assert (
        updated.pop("orchestratorRules") == config["orchestratorRules"] + "\n\n" + RULES
    )
    assert updated == {
        key: value for key, value in config.items() if key != "orchestratorRules"
    }
    assert config["orchestratorRules"] == "Preserve existing approval rules."
    assert rule_config(rule_config(config)) == rule_config(config)
    assert rule_config({}) == {"orchestratorRules": RULES}
    print("PASS: request ancestry; prompt-only delta; config preservation; idempotence")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--self-check", action="store_true")
    modes.add_argument("--apply-rules", action="store_true")
    parser.add_argument("--transcript", type=Path)
    parser.add_argument("--since")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.self_check:
        self_check()
        return
    if args.apply_rules:
        apply_rules()
        return
    if not all((args.transcript, args.since, args.output)):
        parser.error("--transcript, --since and --output are required")
    with args.transcript.open() as source:
        identity, original = recorded_request(map(json.loads, source), args.since)
    if original.get("output_config", {}).get("effort") != "max":
        raise ValueError("The baseline request must use max effort")
    with urllib.request.urlopen(
        "http://127.0.0.1:3001/api/v1/projects/og-workflow", timeout=15
    ) as response:
        env = json.load(response)["project"]["config"]["env"]
    if env["ANTHROPIC_BASE_URL"].rstrip("/") != "http://127.0.0.1:20128":
        raise ValueError("Refusing a route other than the configured local 9router")
    # ponytail: one model step, no tools; confirm usefulness with live completed turns.
    with args.output.open("x") as output, ThreadPoolExecutor(max_workers=2) as pool:
        print(
            json.dumps(
                {
                    "request_id": identity,
                    "request_sha256": hashlib.sha256(
                        json.dumps(original, sort_keys=True).encode()
                    ).hexdigest(),
                    "rules_sha256": hashlib.sha256(RULES.encode()).hexdigest(),
                    "effort": "max",
                    "tool_execution": False,
                }
            ),
            file=output,
            flush=True,
        )
        futures = [
            pool.submit(run_request, label, body, env)
            for label, body in (
                ("baseline", original),
                ("completion-rule", candidate(original)),
            )
        ]
        for future in futures:
            result = future.result()
            print(json.dumps(result), file=output, flush=True)
            print(json.dumps(result), flush=True)


if __name__ == "__main__":
    main()
