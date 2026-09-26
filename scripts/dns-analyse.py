#!/usr/bin/env python3
"""Ask a local model what is notable in recent DNS traffic.

The model is given a digest built from the API's aggregations, never raw
logs. A window holds far more lines than fit in a prompt, the counts are
what carry the signal, and a digest costs the same whether the window held a
thousand messages or ten million.

Settings come from /etc/syslogc-llm.conf (see deploy/llm/dns-analyse.conf),
and any of them can be overridden per run:

    WINDOW=now-15m dns-analyse
    OLLAMA_MODEL=llama3.1:8b dns-analyse --digest-only
"""
import json
import os
import pathlib
import sys
import urllib.error
import urllib.request

CONFIG = os.environ.get("SYSLOGC_LLM_CONFIG", "/etc/syslogc-llm.conf")


def configure():
    """Read KEY=value settings, letting the environment win.

    The same file is an EnvironmentFile for the systemd timer, so a setting
    is written once and means the same thing however the tool is started.
    """
    try:
        for line in pathlib.Path(CONFIG).read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, value = line.split("=", 1)
            os.environ.setdefault(key.strip(), value.strip().strip("\"'"))
    except FileNotFoundError:
        pass


configure()

API = os.environ.get("SYSLOGC_URL", "http://127.0.0.1:8080")
# The key is read from a file rather than the environment: an environment
# variable is visible to every process on the box through ps.
KEY = (os.environ.get("SYSLOGC_KEY")
       or pathlib.Path(os.environ.get("SYSLOGC_KEY_FILE", "/etc/syslogc-llm.key")).read_text().strip())
MODEL = os.environ.get("OLLAMA_MODEL", "qwen3:8b")
OLLAMA = os.environ.get("OLLAMA_URL", "http://127.0.0.1:11434")
WINDOW = os.environ.get("WINDOW", "now-1h")


def api(path, body):
    req = urllib.request.Request(
        API + path,
        data=json.dumps(body).encode(),
        headers={"Authorization": "Bearer " + KEY, "Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=120) as r:
        return json.loads(r.read())


def top(field, limit=12):
    rng = {"time_range": {"from": WINDOW, "to": "now"}}
    body = dict(rng, group_by=field, limit=limit,
                filter={"op": "exists", "field": "dns.qname"})
    return [(r["value"], int(r["metric"])) for r in api("/api/v1/analytics/breakdown", body).get("rows", [])]


def unique_clients(field, limit=12):
    """Distinct clients per value: one client asking a thousand times is one
    client, which is what separates a busy host from a widespread service."""
    rng = {"time_range": {"from": WINDOW, "to": "now"}}
    body = dict(rng, group_by=field, limit=limit,
                metric={"type": "count_distinct", "field": "dns.client_ip"},
                filter={"op": "exists", "field": "dns.qname"})
    return [(r["value"], int(r["metric"])) for r in api("/api/v1/analytics/breakdown", body).get("rows", [])]


def digest():
    lines = [f"DNS traffic digest for {WINDOW} to now.", ""]
    lines.append("Busiest query names (queries, distinct clients):")
    clients = dict(unique_clients("dns.qname"))
    for name, n in top("dns.qname"):
        lines.append(f"  {name}  queries={n}  clients={clients.get(name, '?')}")
    lines.append("")
    lines.append("Busiest client addresses (queries each):")
    for ip, n in top("dns.client_ip", 10):
        lines.append(f"  {ip}  queries={n}")
    lines.append("")
    lines.append("Query types seen:")
    for t, n in top("dns.qtype", 8):
        lines.append(f"  {t}  {n}")
    return "\n".join(lines)


PROMPT = """You are a DNS security analyst reviewing one hour of resolver logs.

{digest}

Answer in at most six sentences:
1. What is the traffic mostly made of?
2. Anything that looks like automated or suspicious behaviour, and which client?
3. One thing worth checking next.

Be concrete and cite the names or addresses. If nothing looks wrong, say so plainly rather than inventing a concern."""


def fail(what, err):
    """Say which part failed and what to check, rather than a traceback."""
    hint = {
        "the API": f"is {API} reachable, and does the key in "
                   f"{os.environ.get('SYSLOGC_KEY_FILE', '/etc/syslogc-llm.key')} still exist?",
        "the model": f"is {MODEL} pulled? `ollama list` on {OLLAMA}, or `ollama pull {MODEL}`",
    }[what]
    print(f"could not reach {what}: {err}\n  {hint}", file=sys.stderr)
    raise SystemExit(1)


def main():
    try:
        text = digest()
    except (urllib.error.URLError, urllib.error.HTTPError, OSError) as e:
        fail("the API", e)
    if "--digest-only" in sys.argv:
        print(text)
        return
    # The HTTP API rather than `ollama run`: the terminal client redraws its
    # output, which mangles anything piped or logged.
    req = urllib.request.Request(
        OLLAMA + "/api/generate",
        data=json.dumps({
            "model": MODEL,
            "prompt": PROMPT.format(digest=text),
            "stream": False,
            "think": False,
            "options": {"temperature": 0.2},
        }).encode(),
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=900) as r:
            print(json.loads(r.read())["response"].strip())
    except (urllib.error.URLError, urllib.error.HTTPError, OSError) as e:
        fail("the model", e)


if __name__ == "__main__":
    main()
