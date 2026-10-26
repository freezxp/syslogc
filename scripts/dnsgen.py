#!/usr/bin/env python3
"""Synthetic DNS resolver logs, healthy or in the middle of an incident.

Imitates BIND 9 (named), dnsmasq and Unbound query logging on two resolvers,
so a collector sees a realistic mix of formats, severities and fields rather
than one shape repeated.

    ./scripts/dnsgen.py --rate 50
    ./scripts/dnsgen.py --incident upstream-down --for 10m
    ./scripts/dnsgen.py --cycle clear=20m,upstream-down=5m,clear=20m,servfail-storm=8m

The point of the incidents is that a dashboard, an alert or a saved search
can be watched changing and then recovering. A generator that only ever
produces healthy traffic proves nothing about the thing you built to notice
when traffic stops being healthy.

Scenarios are listed by --list. "clear" is the healthy baseline; it still
carries the small background of NXDOMAIN, RPZ blocks and the occasional
SERVFAIL that a real resolver always has, because an alert that fires on
those was never going to survive contact with production.

This sends to a collector; it does not resolve anything. Point it at a
development box: the messages are indistinguishable from real resolver logs
once stored, which is the point, and is also why they do not belong in a
system of record.
"""
import argparse
import os
import random
import re
import socket
import sys
import time
from datetime import datetime

RESOLVERS = ["dns01", "dns02"]

# Popular destinations, weighted so a few domains dominate like real traffic.
POPULAR = [
    ("www.google.com", 60), ("clients4.google.com", 20), ("fonts.gstatic.com", 15),
    ("outlook.office365.com", 40), ("login.microsoftonline.com", 30), ("teams.microsoft.com", 25),
    ("update.microsoft.com", 12), ("windowsupdate.microsoft.com", 10),
    ("api.github.com", 8), ("github.com", 6), ("registry.npmjs.org", 5),
    ("s3.amazonaws.com", 14), ("cloudfront.net", 9), ("api.slack.com", 11),
    ("www.youtube.com", 22), ("i.ytimg.com", 18), ("cdn.jsdelivr.net", 4),
    ("time.cloudflare.com", 7), ("one.one.one.one", 3),
    ("www.tiktok.com", 16), ("www.facebook.com", 14), ("www.instagram.com", 10),
    ("mail.time.com.my", 30), ("intranet.time.com.my", 18), ("vpn.time.com.my", 9),
    ("dc01.corp.time.com.my", 26), ("dc02.corp.time.com.my", 24),
    ("printer-hq.corp.time.com.my", 5), ("nas01.corp.time.com.my", 6),
]
POPULAR_NAMES = [name for name, weight in POPULAR for _ in range(weight)]

BLOCKED = ["ads.doubleclick.net", "tracker.adnxs.com", "malware-c2.example.ru",
           "phish-login-microsoft.example.tk", "coin-hive.example.io"]
TYPOS = ["wwww.google.com", "intranet.time.com.mya", "dc03.corp.time.com.my",
         "oldserver.corp.time.com.my", "wpad.corp.time.com.my"]

QTYPES = ["A"] * 55 + ["AAAA"] * 25 + ["HTTPS"] * 8 + ["PTR"] * 5 + ["MX"] * 3 + ["TXT"] * 2 + ["SRV"] * 2
FLAGS = ["+E(0)K", "+", "-E(0)K", "+E(0)DK", "+ED"]
UPSTREAMS = ["192.168.0.11", "192.168.0.12"]

FACILITY = 3  # daemon
SEV_INFO, SEV_NOTICE, SEV_WARNING, SEV_ERROR, SEV_CRIT = 6, 5, 4, 3, 2


# --- what an incident changes -------------------------------------------------
#
# A scenario is a set of dials rather than a separate code path, so healthy and
# broken traffic come out of the same generator and differ only in the ways a
# real failure differs. Anything a scenario does not set keeps its healthy
# value.

SCENARIOS = {
    "clear": {
        "what": "healthy traffic, with the background of failures a real resolver always has",
    },
    "upstream-down": {
        "what": "the forwarders stop answering: timeouts, then SERVFAIL for everything not cached",
        # Nearly everything that has to leave the building fails.
        "servfail": 0.75,
        "timeouts": True,
        "operational_rate": 0.08,
        "operational_kinds": ["upstream-timeout", "upstream-unreachable"],
    },
    "servfail-storm": {
        "what": "a broken delegation or DNSSEC validation failure: a slice of names stop resolving",
        "servfail": 0.35,
        "operational_rate": 0.04,
        "operational_kinds": ["dnssec-bogus", "lame-server"],
    },
    "resolver-silent": {
        "what": "one resolver stops logging entirely — no errors at all, just absence",
        # The subtle one. Nothing looks wrong in what arrives; what matters is
        # what stopped arriving, which is the hardest kind of outage to see
        # and the reason a per-source rate is worth watching.
        "silent_host": "dns02",
    },
    "client-flood": {
        "what": "one client hammering the resolver, until rate limiting starts denying it",
        "flood_client": True,
        "operational_rate": 0.05,
        "operational_kinds": ["rate-limit"],
    },
    "malware-beacon": {
        "what": "a few clients beaconing to blocked domains, so RPZ hits spike",
        "blocked_rate": 0.30,
        "beacon_clients": True,
    },
}


def parse_duration(text: str) -> float:
    """Seconds from 30s, 5m, 2h. Plain numbers are seconds."""
    m = re.fullmatch(r"\s*(\d+(?:\.\d+)?)\s*([smh]?)\s*", text)
    if not m:
        raise argparse.ArgumentTypeError(
            f"{text!r} is not a duration; use 30s, 5m or 2h")
    return float(m.group(1)) * {"": 1, "s": 1, "m": 60, "h": 3600}[m.group(2)]


def parse_cycle(text: str) -> list[tuple[str, float]]:
    """clear=20m,upstream-down=5m into [(scenario, seconds), ...]."""
    out = []
    for part in text.split(","):
        name, _, duration = part.partition("=")
        name = name.strip()
        if name not in SCENARIOS:
            raise argparse.ArgumentTypeError(
                f"unknown scenario {name!r}; --list shows them")
        if not duration:
            raise argparse.ArgumentTypeError(
                f"{name} needs a duration, e.g. {name}=5m")
        out.append((name, parse_duration(duration)))
    if not out:
        raise argparse.ArgumentTypeError("the cycle is empty")
    return out


class World:
    """The generator's state: who is talking, and what is currently broken."""

    def __init__(self, rnd: random.Random):
        self.rnd = rnd
        self.scenario = "clear"
        self.dials = {}
        # Held across a scenario change so a flood or a beacon keeps coming
        # from the same addresses, as it would.
        self.flooder = f"192.168.10.{rnd.randint(2, 254)}"
        self.beacons = [f"192.168.{rnd.choice((10, 20))}.{rnd.randint(2, 254)}" for _ in range(3)]

    def enter(self, scenario: str):
        self.scenario = scenario
        self.dials = {k: v for k, v in SCENARIOS[scenario].items() if k != "what"}

    def dial(self, name, default=None):
        return self.dials.get(name, default)

    def client_ip(self) -> str:
        if self.dial("flood_client") and self.rnd.random() < 0.7:
            return self.flooder
        if self.dial("beacon_clients") and self.rnd.random() < 0.5:
            return self.rnd.choice(self.beacons)
        r = self.rnd.random()
        if r < 0.6:
            return f"192.168.10.{self.rnd.randint(2, 254)}"
        if r < 0.85:
            return f"192.168.20.{self.rnd.randint(2, 254)}"
        if r < 0.95:
            return f"10.20.{self.rnd.randint(0, 3)}.{self.rnd.randint(2, 254)}"
        return f"172.16.{self.rnd.randint(0, 5)}.{self.rnd.randint(2, 254)}"

    def qname(self) -> tuple[str, str]:
        """A name to look up, and what the resolver should answer."""
        rnd = self.rnd
        if rnd.random() < self.dial("blocked_rate", 0.02):
            return rnd.choice(BLOCKED), "blocked"
        # A failing upstream fails whatever was asked for, which is what makes
        # it different from a bad name: the names are fine.
        if rnd.random() < self.dial("servfail", 0.01):
            return rnd.choice(POPULAR_NAMES), "servfail"
        r = rnd.random()
        if r < 0.03:
            return rnd.choice(TYPOS), "nxdomain"
        if r < 0.05:
            return "".join(rnd.choices("abcdefghijklmnopqrstuvwxyz0123456789",
                                       k=rnd.randint(10, 22))) + ".example.net", "nxdomain"
        if r < 0.07:
            octets = [str(rnd.randint(1, 254)) for _ in range(4)]
            return ".".join(reversed(octets)) + ".in-addr.arpa", "ptr"
        return rnd.choice(POPULAR_NAMES), "noerror"


def named_lines(w: World, ip, name, outcome, qtype):
    """BIND 9 query logging, one or two lines per query."""
    rnd = w.rnd
    port = rnd.randint(1024, 65535)
    view = "internal" if ip.startswith(("192.168.", "10.")) else "external"
    tag = f"client @0x{rnd.getrandbits(48):012x} {ip}#{port} ({name}):"
    lines = [(SEV_INFO, "named",
              f"{tag} view {view}: query: {name} IN {qtype} {rnd.choice(FLAGS)} "
              f"({rnd.choice(UPSTREAMS)})")]
    if outcome == "blocked":
        lines.append((SEV_NOTICE, "named",
                      f"{tag} rpz QNAME Local-Data rewrite {name} via {name}.rpz-block.time.com.my"))
    elif outcome == "nxdomain":
        lines.append((SEV_INFO, "named",
                      f"{tag} query failed (NXDOMAIN) for {name}/IN/{qtype} at query.c:7887"))
    elif outcome == "servfail":
        if w.dial("timeouts") and rnd.random() < 0.5:
            lines.append((SEV_WARNING, "named",
                          f"{tag} timed out resolving '{name}/{qtype}/IN': "
                          f"{rnd.choice(UPSTREAMS)}#53"))
        lines.append((SEV_WARNING, "named",
                      f"{tag} query failed (SERVFAIL) for {name}/IN/{qtype} at query.c:7887"))
    if w.dial("flood_client") and ip == w.flooder and rnd.random() < 0.3:
        lines.append((SEV_NOTICE, "named",
                      f"client {ip}#{port}: rate limit drop response to {ip}/32 for {name} IN {qtype}"))
    return lines


def dnsmasq_lines(w: World, ip, name, outcome, qtype):
    """dnsmasq on the branch resolver: the query, then the forward or reply."""
    rnd = w.rnd
    lines = [(SEV_INFO, "dnsmasq", f"query[{qtype}] {name} from {ip}")]
    if outcome == "blocked":
        lines.append((SEV_INFO, "dnsmasq", f"config {name} is 0.0.0.0"))
    elif outcome == "nxdomain":
        lines.append((SEV_INFO, "dnsmasq", f"forwarded {name} to {rnd.choice(UPSTREAMS)}"))
        lines.append((SEV_INFO, "dnsmasq", f"reply {name} is NXDOMAIN"))
    elif outcome == "servfail":
        lines.append((SEV_INFO, "dnsmasq", f"forwarded {name} to {rnd.choice(UPSTREAMS)}"))
        if w.dial("timeouts"):
            lines.append((SEV_WARNING, "dnsmasq",
                          f"no responses from nameserver {rnd.choice(UPSTREAMS)}#53"))
        lines.append((SEV_WARNING, "dnsmasq", f"reply {name} is SERVFAIL"))
    else:
        answer = f"{rnd.randint(1, 223)}.{rnd.randint(0, 255)}.{rnd.randint(0, 255)}.{rnd.randint(1, 254)}"
        if rnd.random() < 0.5:
            lines.append((SEV_INFO, "dnsmasq", f"cached {name} is {answer}"))
        else:
            lines.append((SEV_INFO, "dnsmasq", f"forwarded {name} to {rnd.choice(UPSTREAMS)}"))
            lines.append((SEV_INFO, "dnsmasq", f"reply {name} is {answer}"))
    return lines


def unbound_lines(w: World, ip, name, outcome, qtype):
    rcode = {"blocked": "NXDOMAIN", "nxdomain": "NXDOMAIN", "servfail": "SERVFAIL"}.get(outcome, "NOERROR")
    rnd = w.rnd
    # A failing lookup takes the full timeout rather than answering quickly,
    # which is what makes a latency panel move before an error count does.
    elapsed = rnd.uniform(1.5, 5.0) if outcome == "servfail" else rnd.uniform(0.0002, 0.35)
    return [(SEV_INFO, "unbound",
             f"[{rnd.randint(1000, 9999)}:0] info: {ip} {name}. {qtype} IN {rcode} "
             f"{elapsed:.6f} {rnd.randint(0, 1)} {rnd.randint(40, 220)}")]


def operational_line(w: World):
    """Events that are not a query: the resolver talking about itself."""
    rnd = w.rnd
    kinds = w.dial("operational_kinds") or [
        "notify", "transfer", "lame-server", "denied", "entropy", "refused", "reload", "keys"]
    kind = rnd.choice(kinds)
    upstream = rnd.choice(UPSTREAMS)
    return {
        "notify": (SEV_NOTICE, "named",
                   f"zone corp.time.com.my/IN/internal: sending notifies (serial {rnd.randint(2026102600, 2026102699)})"),
        "transfer": (SEV_INFO, "named",
                     f"zone corp.time.com.my/IN/internal: transferred serial {rnd.randint(2026102600, 2026102699)}"),
        "lame-server": (SEV_WARNING, "named",
                        f"lame server resolving '{rnd.choice(POPULAR_NAMES)}' (in 'example.net'?): "
                        f"198.51.100.{rnd.randint(2, 250)}#53"),
        "denied": (SEV_WARNING, "named",
                   f"client {w.client_ip()}#{rnd.randint(1024, 65535)}: "
                   f"query (cache) '{rnd.choice(POPULAR_NAMES)}/A/IN' denied"),
        "entropy": (SEV_ERROR, "named", "could not open entropy source /dev/random: file not found"),
        "refused": (SEV_ERROR, "named",
                    f"REFUSED unexpected RCODE resolving '{rnd.choice(TYPOS)}/A/IN': "
                    f"203.0.113.{rnd.randint(2, 250)}#53"),
        "reload": (SEV_NOTICE, "systemd", "Reloaded BIND Domain Name Server."),
        "keys": (SEV_INFO, "named",
                 f"managed-keys-zone: Key {rnd.randint(10000, 60000)} for zone . is now trusted"),
        # Incident-only kinds below.
        "upstream-timeout": (SEV_WARNING, "unbound",
                             f"[{rnd.randint(1000, 9999)}:0] warning: unable to reach {upstream} - timeout"),
        "upstream-unreachable": (SEV_ERROR, "named",
                                 f"network unreachable resolving '{rnd.choice(POPULAR_NAMES)}/A/IN': {upstream}#53"),
        "dnssec-bogus": (SEV_ERROR, "named",
                         f"validating {rnd.choice(POPULAR_NAMES)}/A: no valid signature found "
                         f"(resolving '{rnd.choice(POPULAR_NAMES)}/A/IN')"),
        "rate-limit": (SEV_NOTICE, "named",
                       f"client {w.flooder}#{rnd.randint(1024, 65535)}: "
                       f"rate limit slip response to {w.flooder}/32 for . IN ANY"),
    }[kind]


def message(host: str, severity: int, app: str, text: str) -> bytes:
    """RFC 3164: <PRI>Mmm dd hh:mm:ss host app[pid]: message."""
    pri = FACILITY * 8 + severity
    stamp = datetime.now().strftime("%b %e %H:%M:%S")
    pid = {"named": 1247, "dnsmasq": 903, "unbound": 1580, "systemd": 1}.get(app, 1000)
    return f"<{pri}>{stamp} {host} {app}[{pid}]: {text}".encode("utf-8", "replace")


class Sender:
    """One connection to the collector, reopened when it breaks."""

    def __init__(self, target, port, protocol):
        self.target, self.port, self.protocol = target, port, protocol
        self.sock = self._open()

    def _open(self):
        if self.protocol == "tcp":
            s = socket.create_connection((self.target, self.port), timeout=10)
            s.settimeout(None)
            return s
        return socket.socket(socket.AF_INET, socket.SOCK_DGRAM)

    def send(self, payload: bytes) -> bool:
        try:
            if self.protocol == "tcp":
                self.sock.sendall(payload + b"\n")
            else:
                self.sock.sendto(payload, (self.target, self.port))
            return True
        except OSError:
            time.sleep(2)
            try:
                self.sock.close()
            except OSError:
                pass
            self.sock = self._open()
            return False


def schedule(args) -> list[tuple[str, float]]:
    """What to run, in order. An empty duration means for ever."""
    if args.cycle:
        return args.cycle
    if args.incident:
        # An incident, then back to healthy traffic, so recovery is visible.
        return [(args.incident, args.duration or 600.0), ("clear", 0.0)]
    return [("clear", 0.0)]


def main() -> int:
    p = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--target", default=os.environ.get("DNSLOG_TARGET", "127.0.0.1"),
                   help="collector host (default: 127.0.0.1)")
    p.add_argument("--port", type=int, default=int(os.environ.get("DNSLOG_PORT", "514")))
    p.add_argument("--protocol", default=os.environ.get("DNSLOG_PROTOCOL", "udp"),
                   choices=["udp", "tcp"])
    p.add_argument("--rate", type=float, default=float(os.environ.get("DNSLOG_RATE", "15")),
                   help="messages per second, averaged (default: 15)")
    p.add_argument("--seed", type=int, default=int(os.environ.get("DNSLOG_SEED", "0")),
                   help="fixed seed for a repeatable run (default: random)")
    p.add_argument("--incident", choices=sorted(SCENARIOS), metavar="NAME",
                   help="run this scenario, then return to healthy traffic")
    p.add_argument("--for", dest="duration", type=parse_duration, metavar="DURATION",
                   help="how long the incident lasts (default: 10m)")
    p.add_argument("--cycle", type=parse_cycle, metavar="SPEC",
                   help="repeat a sequence, e.g. clear=20m,upstream-down=5m")
    p.add_argument("--list", action="store_true", help="describe the scenarios and exit")
    args = p.parse_args()

    if args.list:
        width = max(len(n) for n in SCENARIOS)
        for name in sorted(SCENARIOS):
            print(f"  {name:<{width}}  {SCENARIOS[name]['what']}")
        return 0

    rnd = random.Random(args.seed or None)
    world = World(rnd)
    sender = Sender(args.target, args.port, args.protocol)
    plan = schedule(args)

    print(f"sending to {args.target}:{args.port}/{args.protocol} at ~{args.rate}/s", file=sys.stderr)
    sent = 0
    started = time.monotonic()
    step = 0
    while True:
        name, seconds = plan[step % len(plan)]
        world.enter(name)
        until = time.monotonic() + seconds if seconds else None
        print(f"[{datetime.now():%H:%M:%S}] {name}: {SCENARIOS[name]['what']}"
              + (f" (for {seconds:.0f}s)" if seconds else ""), file=sys.stderr)

        while until is None or time.monotonic() < until:
            host = rnd.choice(RESOLVERS)
            # A silent resolver is an absence, not an error: it simply stops
            # saying anything, and nothing in the stream says why.
            if host == world.dial("silent_host"):
                sent += 1
                _pace(started, sent, args.rate, rnd)
                continue

            ip = world.client_ip()
            qname, outcome = world.qname()
            qtype = "PTR" if outcome == "ptr" else rnd.choice(QTYPES)
            if outcome == "ptr":
                outcome = "noerror"

            if rnd.random() < world.dial("operational_rate", 0.01):
                batch = [operational_line(world)]
            elif host == "dns01":
                batch = named_lines(world, ip, qname, outcome, qtype)
            elif rnd.random() < 0.5:
                batch = dnsmasq_lines(world, ip, qname, outcome, qtype)
            else:
                batch = unbound_lines(world, ip, qname, outcome, qtype)

            for severity, app, text in batch:
                if sender.send(message(host, severity, app, text)):
                    sent += 1
            _pace(started, sent, args.rate, rnd)
        step += 1


def _pace(started: float, sent: int, rate: float, rnd: random.Random):
    """Hold the long-run average near rate, with jitter so it is not a beat."""
    if rate <= 0:
        return
    delay = (started + sent / rate) - time.monotonic()
    if delay > 0:
        time.sleep(delay * rnd.uniform(0.5, 1.5))


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        sys.exit(130)
