# dnswizard

```
     _                        _                        _
  __| |_ __  _____      _____(_)______ _ _ __ __ _  __| |
 / _` | '_ \/ __\ \ /\ / /_  / |_  / _` | '__/ _` |/ _` |
| (_| | | | \__ \\ V  V / / /| |/ / (_| | | | (_| | (_| |
 \__,_|_| |_|___/ \_/\_/ /___|_/___\__,_|_|  \__,_|\__,_|
```

A configurable DNS proxy for lab and development work. Point a name at your
laptop without editing `/etc/hosts` on every machine, and watch what an
application actually resolves.

Single static binary, no runtime to install. Runs on Linux, macOS, Windows and
BSD.

## Install

```sh
go install github.com/joda32/dnswizard@latest
```

Or from a checkout:

```sh
make build          # ./dnswizard
make build-all      # binaries for linux/darwin/windows on amd64 and arm64
make install        # into $(go env GOPATH)/bin
```

## Quick start

Run it as a plain forwarding proxy that logs everything — the fastest way to
find out what an application is looking up:

```sh
sudo dnswizard serve
```

Point a wildcard domain at your machine, no config file needed:

```sh
sudo dnswizard serve -r '*.dev.local=127.0.0.1'
```

```
22:52:00 INF  listening proto=udp addr=127.0.0.1:53
22:52:00 INF  listening proto=tcp addr=127.0.0.1:53
22:52:01 INF  cooked  client=127.0.0.1 name=api.dev.local. type=A answer=127.0.0.1
22:52:01 INF  proxied client=127.0.0.1 name=example.com. type=A upstream=1.1.1.1:53 rcode=NOERROR answers=2 rtt=6.1ms
```

Then tell the machine to use it — add `nameserver 127.0.0.1` at the top of
`/etc/resolv.conf`, or set it in your network settings.

No root? Bind an unprivileged port and query it explicitly:

```sh
dnswizard serve -l 127.0.0.1:5353 -r '*.dev.local=127.0.0.1'
dnswizard query api.dev.local -s 127.0.0.1:5353
```

## Configuration

```sh
dnswizard config init          # writes a commented dnswizard.yaml
dnswizard serve                # picks up ./dnswizard.yaml automatically
```

```yaml
listen:
  - 127.0.0.1:53               # bare address binds both UDP and TCP

upstream:                      # tried in order, with failover
  - 1.1.1.1:53
  - 8.8.8.8:53

ttl: 60
fallback: proxy                # proxy | nxdomain | refused | empty

hosts:                         # shorthand: A or AAAA picked automatically
  "*.dev.local": 127.0.0.1
  "api.dev.local":
    - 10.0.0.10
    - 10.0.0.11

records:                       # everything else
  - name: dev.local
    type: MX
    value: mail.dev.local      # preference defaults to 10
  - name: _sip._tcp.dev.local
    type: SRV
    value: "0 5 5060 sip.dev.local"
  - name: dev.local
    type: TXT
    value: "v=spf1 -all"
    ttl: 300
```

The file is re-read when it changes, so editing records takes effect without a
restart. A config that fails to parse is logged and ignored — the running
record set stays in place.

Command-line flags override the file. `dnswizard config check` validates a file
and prints the records it would serve.

### Record types

`A` `AAAA` `CNAME` `MX` `NS` `PTR` `TXT` `SOA` `SRV` `NAPTR` `CAA` `DNSKEY`
`RRSIG` — and anything else [miekg/dns](https://github.com/miekg/dns) can parse
(`HTTPS`, `SVCB`, `TLSA`, …) if you write the value in zone-file syntax.

Values use a friendly shorthand where a zone file would be fussy: bare hostnames
for `MX`, unquoted `TXT` strings (split into 255-byte chunks automatically),
unquoted `NAPTR` fields, and target names without a trailing dot.

### Name matching

Patterns are matched most-specific-first:

| Pattern | Matches | Does not match |
|---|---|---|
| `api.dev.local` | `api.dev.local` | `v2.api.dev.local` |
| `*.dev.local` | `api.dev.local`, `v2.api.dev.local` | `dev.local` |
| `a.*.dev.local` | `a.b.dev.local` | `a.b.c.dev.local` |
| `*` | everything | — |

A leading `*` spans one or more labels; a `*` in any other position matches
exactly one. An exact name beats a wildcard, and an explicit `CNAME` beats a
broader wildcard address record at the same name.

### Choosing what gets faked

```sh
# Fake only these names, resolve everything else for real
dnswizard serve -r '*=127.0.0.1' --only dev.local,*.dev.local

# Fake everything except these
dnswizard serve -r '*=127.0.0.1' --except github.com,*.docker.io
```

## Commands

| Command | Purpose |
|---|---|
| `dnswizard serve` | run the server |
| `dnswizard query <name> [type]` | send one query, a small stand-in for `dig` |
| `dnswizard config init` | write a starter config |
| `dnswizard config check` | validate a config and list its records |
| `dnswizard config import <file.ini>` | convert a legacy INI record file |

Run `dnswizard serve --help` for the full flag list.

## Migrating an older setup

An INI record file — a section per record type, one `domain=value` pair per
line — converts directly:

```sh
dnswizard config import records.ini -o dnswizard.yaml
```

Flag equivalents, if you are coming from a Python-era invocation:

| Old flag | dnswizard |
|---|---|
| `--fakeip 127.0.0.1` | `-r '*=127.0.0.1'` |
| `--fakeip X --fakedomains a.com` | `-r 'a.com=X' -r '*.a.com=X'` |
| `--fakeipv6 ::1` | `-r 'AAAA:*=::1'` |
| `--fakemail mail.x.com` | `-r 'MX:*=mail.x.com'` |
| `--fakealias www.x.com` | `-r 'CNAME:*=www.x.com'` |
| `--fakens ns.x.com` | `-r 'NS:*=ns.x.com'` |
| `--file records.ini` | `-c dnswizard.yaml` (after `config import`) |
| `--fakedomains a.com` | `--only a.com` |
| `--truedomains a.com` | `--except a.com` |
| `--nameservers 4.2.2.1#53#tcp` | `-u tcp://4.2.2.1:53` (old syntax still parses) |
| `-i 0.0.0.0 -p 5353` | `-l 0.0.0.0:5353` |
| `-t` / `--tcp` | not needed — UDP and TCP are both served |
| `-6` / `--ipv6` | not needed — `-l '[::1]:53'` |
| `--logfile FILE` | `--log-file FILE` |
| `-q` | `-q` (hides the banner) |

### Behaviour that changed on purpose

- **Wildcards are stricter.** The old matcher compared only as many labels as
  the shorter of pattern and query, so `example.com` silently matched every
  subdomain and `*.example.com` matched the bare apex. Here those are two
  distinct patterns. Add both if you want both.
- **UDP and TCP run together**, rather than one or the other.
- **Upstreams fail over.** Picking one at random with no retry meant a dead
  server caused intermittent failures. Truncated UDP replies are also retried
  over TCP automatically.
- **Locally known names do not leak.** If a name has records of some type but
  not the queried one, dnswizard answers NODATA instead of forwarding an
  internal name to a public resolver. Set `nodata_for_known_names: false` for
  the old behaviour.
- **CNAMEs are followed.** An A/AAAA query for a name with a local CNAME
  returns the alias plus the target's addresses, resolving the target upstream
  if it is not local. Set `chase_cname: false` to disable.
- **New upstream transports.** `tcp://` and DNS-over-TLS via
  `tls://1.1.1.1:853#cloudflare-dns.com`.
- **`fallback`.** Unmatched queries can answer NXDOMAIN, REFUSED or empty
  instead of being proxied — useful for an offline lab.

## Development

```sh
make test        # go test ./...
make lint        # go vet + gofmt check
make build-all   # cross-compile
```

Planned work is in [BACKLOG.md](BACKLOG.md).

## Licence

Copyright (C) 2026 Willem Mouton. Released under the **GNU Affero General
Public License v3.0** — see [LICENSE](LICENSE).

In short: use it for anything, including paid client work and internal company
use, at no cost and without asking. If you want to **build it into a product
you sell, or offer it as a hosted service**, without releasing that work's
source under the AGPL, you need a separate commercial licence — see
[COMMERCIAL.md](COMMERCIAL.md). I grant those on reasonable terms, and for free
to non-profits, education, and open source projects that cannot use the AGPL.

dnswizard was inspired by the Python-era DNS proxies that came before it, whose
authors worked out much of what a tool like this should do. This is an
independent implementation and shares no code with them.
