# AGENTS.md — guidance for agents and contributors

Self-hosted Go server that replaces vTuner for network receivers (Denon,
Marantz, Yamaha…). A port of [YCast](https://github.com/milaq/YCast) (Python),
GPLv3. The ultimate goal: serve XML that tiny, buggy XML parsers embedded in
audio receivers can digest.

## Commands

```sh
go build -trimpath -ldflags "-s -w" -o ycast-go ./cmd/ycast-go   # build
go vet ./...                              # must stay clean
gofmt -l cmd internal                # must stay empty (vendored code excluded)
golangci-lint run ./...                   # must stay clean (see .golangci.yml)
go test -race ./...                       # all tests must pass
```

No Makefile; this is a single-package project, those commands are all there
is. `golangci-lint` is pinned in `mise.toml` (v2, config `.golangci.yml`) and
runs in CI from the checksum-pinned official binary. The config is
deliberately lean — see `docs/DECISIONS.md` #16/#17 for what is enabled,
what is disabled and why (each `gosec`/`errcheck` exclusion is justified
there); rejected findings are fixed in code or silenced with a targeted,
explained `//nolint`, never by growing the exclusion list by default.

## Non-negotiable rules

1. **Curated dependencies, modern Go** — dependencies are allowed and
   preferred when they improve readability, correctness or feature coverage
   (e.g. `gopkg.in/yaml.v3`, `golang.org/x/image`). The standard library is
   still the first choice when it is sufficient. Rules for adding a
   dependency:
   - well-maintained, widely adopted, no transitive dependency bloat
     (`go mod graph` must stay small);
   - `golang.org/x/...` modules are the natural first candidates;
   - justify the addition with an entry in `docs/DECISIONS.md`;
   - cross-compilation for Raspberry Pi / MIPS routers must keep working
     (pure Go, no cgo).

   The one exception that stays hand-written is the vTuner XML in
   `internal/vtuner` — that is a byte-for-byte compatibility constraint, not a
   library question (see rule 2 and `docs/DECISIONS.md`).
2. **Byte-for-byte compatibility** — the vTuner XML (`internal/vtuner`) and
   the station IDs (`internal/station`, MD5 checksum) reproduce the output of
   the original
   Python code exactly. Do not "modernise" or "clean up": presets stored on
   receivers and their parsers depend on them. The seemingly odd bits
   (`?vtuner=true`, `Content-Type: text/html`, `Transfer-Encoding: identity`,
   whitespace-free XML) are documented workarounds for real hardware bugs.
   Each one has a comment explaining why; keep those comments up to date when
   you touch the code.
3. **Behaviour change = test + README** — every visible change must be covered
   by a test next to the code it covers and documented in `README.md` (including the
   limits — see the "Differences" section and the honesty about what has not
   been tested).
4. **Security** — station IDs end up in file paths and upstream URLs: keep the
   `validStationID` validation. The `/ycast/stream` relay must only accept IDs
   of known stations, never an arbitrary URL. Cap sizes (reads, icons, caches).

## Style

- Idiomatic, modern Go: use recent stdlib (`slog`, `errors.Join`, generics,
  `min`/`max`, `http.ServeMux` patterns, `http.ResponseController`) and
  vetted libraries over hand-rolled code. `gofmt`-formatted, silent
  `go vet`.
- Comments explain the *why* (receiver quirks, compatibility choices), not the
  *what*. Documentation and comments are in English.
- Errors: degrade gracefully rather than crash (Radio Browser down → empty
  directory; invalid `stations.yml` → keep serving the last valid version).
  Log with `slog`; deduplicate repetitive messages.
- Concurrency: `go test -race` must always pass.

## Things to know

- `stations.yml` is checked on every request (a cheap stat call) and reloaded
  when it changes; parsing is done with `yaml.v3` on an ordered `yaml.Node`
  tree (order preserved, duplicates: last wins) — see `internal/mystations`.
- **Layout** follows the standard `cmd/` + `internal/` convention:
  `cmd/ycast-go` (entry point), `internal/station` (shared type + IDs),
  `internal/vtuner` (XML), `internal/mystations`, `internal/radiobrowser`,
  `internal/icons`, `internal/server` (HTTP handlers + stream relay). Tests
  live next to the code they cover.
- `docs/DECISIONS.md` records structural decisions (a title, the decision, the
  why, the consequences); add an entry for any new decision (format, library,
  protocol, compatibility).
- **CI hygiene** — third-party GitHub Actions are pinned to full commit SHAs
  (version tag kept as a trailing comment); base images are date-pinned (see
  `docs/DECISIONS.md`). Bumping either is a deliberate, reviewable change.
  `.github/dependabot.yml` keeps dependencies and SHA pins fresh (weekly,
  grouped PRs); go.mod and the `vendor/` tree move together — a PR with a
  stale `vendor/` must be fixed with `go mod vendor` before merge.
- **Plumber pin** — the security-policy tool is always pinned to an exact
  version in `mise.toml`; bumps are manual and deliberate: `mise run
  update-plumber` updates the pin, and the policy must be validated with
  the new version (`plumber config validate` + `plumber analyze`) before
  landing. The scan runs in CI via the official `getplumber/plumber`
  action (the only third-party action, allowlisted in `.plumber.yaml`).
  Dependabot does not cover mise `github:` tools — see
  `docs/DECISIONS.md` #18.
- Real hardware (an actual receiver, radio-browser.info in production) has
  never been used for testing: never claim in the README what has not been
  verified.
