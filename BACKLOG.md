# Backlog

Ideas not yet scheduled. Nothing here is committed to a release.

## HTTP control API

Manage records in a running server without editing the config file or
restarting. The motivating case: a test harness or dev script that needs a name
to resolve somewhere for the next thirty seconds, then wants it gone.

Expected to grow well beyond CRUD — treat the sketch below as a starting point,
not a spec.

### Rough shape

```
GET    /v1/records                 list, with ?name= and ?type= filters
POST   /v1/records                 add one or more
DELETE /v1/records/{name}          remove all types at a name
DELETE /v1/records/{name}/{type}   remove one type
PUT    /v1/records                 replace the whole runtime set
GET    /v1/config                  effective config as served
GET    /v1/stats                   the counters already tracked in server.Stats
GET    /v1/healthz                 liveness
```

A streaming query log (`GET /v1/queries` over SSE or a websocket) is the other
obvious one — watching resolution live is half of what the tool is for, and it
is more useful over the wire than in a terminal.

### Design questions to settle first

- **Persistence.** Do API-added records write back to `dnswizard.yaml`, or live
  only in memory until the process exits? Both are defensible; ephemeral is
  probably right by default with an explicit save endpoint.
- **Interaction with hot reload.** This is the sharp edge. Today a file change
  swaps the whole store. If the API has also added records, a reload silently
  discards them. Likely fix: keep two layers — a file-backed store and a
  runtime overlay — and have `Reload` replace only the former. `records.Store`
  is already immutable-and-swapped, so an overlay fits, but `LookupScored`
  needs to resolve across both layers with a defined precedence.
- **TTL on the record vs lifetime of the record.** A record that self-expires
  after N seconds is a different concept from its DNS TTL and probably wants
  its own field.
- **Auth.** Anything that can rewrite DNS answers is worth protecting even on
  loopback. Bearer token from config or env, off by default while bound to
  127.0.0.1, mandatory the moment the bind address is not loopback. Refuse to
  start rather than silently exposing an unauthenticated control plane.
- **Bind address.** Separate listener and port from the DNS service; disabled
  unless configured.
- **CLI over the API.** Once it exists, `dnswizard record add/rm/ls` talking to
  a running server is nearly free and is probably how it will actually get
  used day to day.

### Notes

- Version the path from the first commit (`/v1`); this is going to churn.
- The AGPL's network clause (section 13) bites here: once dnswizard exposes an
  HTTP interface, anyone running a *modified* build that users reach over the
  network owes them its source. That is the intended effect, but it is worth
  saying plainly in the API docs so nobody is surprised.
- Keep the DNS hot path free of new locks — the atomic pointer swap in
  `server.runtime` should stay the only synchronisation.

## File staging over DNS

Serve chunks of a local file through DNS responses, allowing data to be
transferred purely via DNS queries. Inspired by dnschef-ng's implementation of
the same idea.

### How it works

A record's value points to a file instead of an address. The chunk index is
extracted from digits in the queried subdomain — `ns0.example.com` returns
chunk 0, `test12.example.com` returns chunk 12. Supported record types:

- **A** — 4 bytes per chunk, packed into one IPv4 address. Last chunk
  zero-padded if short.
- **AAAA** — 16 bytes per chunk, same padding.
- **TXT** — base64-encoded chunks. Supports a `response_format` template and a
  `response_prefix_pool` (list of strings like `"atlassian-domain-verification="`
  that rotate randomly into the response), so staged data blends in with
  legitimate domain-verification TXT records.

### Config sketch

```yaml
records:
  - name: "*.staging.lab"
    type: A
    file: ./payload.bin
    chunk_size: 4

  - name: "*.exfil.lab"
    type: TXT
    file: ./data.bin
    chunk_size: 189
    response_format: "{prefix}{chunk}"
    response_prefix_pool:
      - "atlassian-domain-verification="
      - "docusign="
```

### Design questions

- **Chunk extraction rule.** dnschef-ng collects all digits from the first label
  (`qname.split('.')[0]`) and joins them. Simple but surprising when a label
  like `test1a2` silently becomes chunk 12. We could require a specific position
  (leading digits only, or a `{n}` placeholder in the pattern) for clarity.
- **File lifetime.** Read once at config load (cheap, predictable, needs reload
  to pick up changes) or on every query (live, but disk I/O on the DNS hot
  path). Read-once with a hot-reload re-read is probably the right call.
- **End-of-file signalling.** The client needs to know when it has all the
  chunks. Options: return NXDOMAIN / empty answer for out-of-range indices, or
  include a total-chunks count somewhere (e.g. a TXT record at a well-known
  name like `_meta.staging.lab`).
- **Integration with the record store.** File-backed records are conceptually a
  different kind of value — they need the file path, chunk size, and optional
  format/prefix fields. This probably means extending `records.Record` with an
  optional `FileStaging` struct rather than overloading the `Value` field.

### Notes

- This is primarily a pentest/red-team feature (data staging and exfiltration
  over DNS). Not directly useful for lab/dev DNS, but it's part of the tool's
  heritage and worth supporting.
- The TXT prefix rotation is specifically an evasion technique — it makes
  responses look like common domain-ownership verification records.
- A Go implementation can memory-map the file or read it into a byte slice once,
  avoiding the per-query disk I/O that dnschef-ng does.

## Unsorted

Things that came up while porting, not yet thought through.

- Response cache for proxied queries, with the log line saying `cached` so it
  is obvious why a lookup was instant.
- DNS-over-HTTPS upstreams, to sit alongside the existing DoT support.
- Serving as an authoritative zone for a suffix (`.dev.local` and friends)
  rather than record-by-record — NS/SOA synthesis, correct NXDOMAIN for
  unknown names under an owned suffix.
- `dnswizard serve --from-docker` or similar: derive records from running
  container names.
- Record sets that round-robin or weight rather than returning every value.
- systemd unit and launchd plist, since this wants to run in the background.
