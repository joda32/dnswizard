# Changelog

## 0.1.0 — unreleased

First release.

Released under the GNU Affero General Public License v3.0. Free for any use,
including paid client work; embedding it in a proprietary product or hosted
service requires a commercial licence (see COMMERCIAL.md).

### Core features

- Fake answers for `A`, `AAAA`, `CNAME`, `MX`, `NS`, `PTR`, `TXT`, `SOA`,
  `SRV`, `NAPTR`, `DNSKEY` and `RRSIG`, using the same value formats
- Wildcard domain matching
- `ANY` queries answered with every known record for a name
- Inclusive and exclusive domain lists (`--only` / `--except`, previously
  `--fakedomains` / `--truedomains`)
- Proxying of unmatched queries to upstream resolvers
- UDP and TCP, IPv4 and IPv6
- Logging to a file
- External configuration file

### New

- Single static binary; no runtime dependency
- YAML configuration, hot-reloaded when the file changes
- `dnswizard config import` converts a legacy INI record file
- UDP and TCP served simultaneously rather than one or the other
- Upstream failover, and automatic TCP retry for truncated UDP replies
- DNS-over-TLS upstreams (`tls://1.1.1.1:853#cloudflare-dns.com`)
- Multiple values per name, all returned in one answer
- Per-record TTLs
- Local `CNAME` chasing, including resolving an external target upstream
- NODATA for locally known names, so internal names stay off the public
  internet
- `fallback` modes: `proxy`, `nxdomain`, `refused`, `empty`
- Any record type miekg/dns can parse, including `CAA`, `HTTPS` and `SVCB`
- `dnswizard query`, a small `dig` stand-in for testing a setup
- `dnswizard config check` validates a config and lists what it would serve
- EDNS0 is honoured and responses are truncated correctly
- Structured logging with console, text and JSON formats

### Changed from the previous Python implementation

- Wildcard matching is stricter: `example.com` no longer implicitly matches
  subdomains, and `*.example.com` no longer matches the bare apex. See the
  README for the full table.
- `-t/--tcp` and `-6/--ipv6` are gone; both transports and both families are
  handled by the listen address.

### Fixed relative to the previous implementation

- A dead upstream no longer causes intermittent failures (random choice with no
  retry)
- Responses larger than 1024 bytes are no longer silently cut off by a
  fixed-size receive buffer
- TCP requests spanning multiple reads are handled
- Malformed and non-query messages get a proper protocol response instead of
  being dropped
