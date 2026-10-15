# Design decisions

A log of ycast-go's structural choices and the reasons behind them. Decisions
are ordered from most structural to most specific; the newest ones at the
bottom of each section. A decision is not removed without an entry explaining
why.

## 1. ~~Single binary, standard library only~~ — SUPERSEDED by #11

Originally the project had no external dependencies at all (hand-written YAML
parser and image resizer). **Superseded**: the project now uses curated,
well-maintained libraries where they beat hand-rolled code (see #11); the
hand-written vTuner XML remains, but as a compatibility decision (#2), not a
dependency decision.

**Decision**: no external dependencies (`go.mod` contains only the module);
everything is written against the stdlib, including the YAML parser and the
image resizing.

**Why**: the server targets small machines (Raspberry Pi, NAS boxes, MIPS
routers) and must cross-compile in one command without an exotic toolchain. A
static binary is copied as-is; no supply chain to audit for a server expected
to run for years without maintenance.

**Consequences** (historical, superseded): the original YAML parser only
handled a subset (categories, `name: url`, quotes, comments) and the icons
had a hand-written box filter; both were replaced in #11.

## 2. Hand-written vTuner XML, byte-for-byte compatible

**Decision**: do not use `encoding/xml`; generate the XML in `internal/vtuner` with
`strings.Builder`, reproducing the reference Python (ElementTree) output
exactly: no whitespace between elements, empty elements as `<X />`, escaping
limited to `&`, `<`, `>`.

**Why**: receivers ship naive XML parsers validated against a single byte
producer. An "equivalent" but differently formatted XML can break the menu.
The served content is also what the tests compare against real Python output.

**Consequences**: any format change must go through a byte-for-byte
comparison (see `TestXMLMatchesPython`).

## 3. Station IDs compatible with YCast Python

**Decision**: the checksum of `stations.yml` stations reuses the original
algorithm (MD5 of `name+url`, one half of the digest XOR-folded onto the
other, first 12 uppercase hex chars) — `internal/station`.

**Why**: presets stored on the receiver reference these IDs; they therefore
remain valid after migrating from YCast Python.

## 4. HTTPS relay by station ID, never an arbitrary URL

**Decision**: `/ycast/stream` only relays the stream of a known station,
resolved by ID (`MY_`/`RB_` prefix + checksum/UUID validated by regex). HTTPS
URLs are rewritten to the relay when `-proxy-https` is on (the default);
otherwise they are downgraded to HTTP, like YCast Python.

**Why**: receivers cannot do TLS and many modern stations are HTTPS-only.
Relaying by ID avoids exposing a generic HTTP proxy on the LAN.

**Consequences**: `Transfer-Encoding: identity` (no chunked encoding — some
embedded players choke on it), no `WriteTimeout` on the server (streams run
for hours), `Icy-*` headers copied through.

## 5. Empirical receiver compatibility

**Decision**: reproduce the quirks receivers expect rather than "clean"
HTTP/XML:

- `Content-Type: text/html` on the XML pages (validated by the reference);
- dummy `?vtuner=true` parameter at the end of URLs, because receivers append
  their parameters with `&` without checking for a `?`;
- paging compatible with the three known parameter variants
  (`startitems/enditems`, `startItems/endItems`, `start/howmany`);
- `/setupapp/...` served as-is: it is what receivers actually request.

**Why**: the goal is to work on existing hardware, not to be a canonical
server. Every workaround is commented in the code.

## 6. stations.yml re-checked on every request

**Decision**: an `os.Stat` on every "My Stations" request, reload when
mtime/size change; on a syntax error the last valid version keeps being
served. Parsing is done with `yaml.v3` (see #11; originally a hand-written
subset parser).

**Why**: hot editing without restart, without a watcher; the file is small and
the stat is negligible against network latency. A half-edited file must never
empty the receiver's menu.

## 7. Radio Browser: DNS discovery, bounded cache, graceful degradation

**Decision**:

- servers discovered through DNS (`all.api.radio-browser.info`, as the project
  recommends), random order, 3 attempts, server list cached for 1 h;
- responses cached for 5 min (64 entries max, simple eviction);
- country/language/genre listings capped at `-max-stations` (default 500),
  most voted first, then sorted by name;
- any Radio Browser error degrades to an empty directory / message, never a 500.

**Why**: the API is a volunteer-run service; be gentle (caches, limits) and
survive its unavailability, including on memory-constrained routers.

## 8. Icons: JPEG ≤ 290 px, disk cache

**Decision**: logos are downloaded (5 MB limit), flattened onto white, shrunk
(longest side ≤ 290 px, hand-written box filter) and re-encoded as JPEG
quality 85, cached on disk (`$XDG_CACHE_HOME/ycast-go/icons`, atomic write via
tmp+rename).

**Why**: this is the format/size range vTuner receivers display reliably; the
cache avoids re-downloading on every menu navigation.

## 9. Logging: slog, debug = requests + responses

**Decision**: text `log/slog` on stderr; `-d` enables the debug level, which
logs every incoming request (method, Host, full URI, source IP) **and** the
body of the XML responses sent to the receiver.

**Why**: debugging hardware compatibility requires seeing the
request → exact XML pair served. Bodies are truncated at 8 KB and the
`/ycast/stream` and `/ycast/icon` routes only log their status (endless
stream, binary) so debug mode cannot exhaust the machine.

## 10. Documented honesty

**Decision**: the README explicitly states what has not been tested on real
hardware (an actual receiver, radio-browser.info in production) and what is
not implemented (favourites, `.m3u`/`.pls` playlists, upstream "ICY 200 OK"
/Shoutcast v1 streams when relayed).

**Why**: a project meant to be picked up and self-hosted; grey areas must be
visible, not hidden behind optimistic documentation.

## 11. Modern Go with curated dependencies (supersedes #1)

**Decision**: dependencies are allowed and preferred when they improve
readability, correctness or feature coverage. Adopted so far:

- `gopkg.in/yaml.v3` — replaces the hand-written YAML subset parser;
  `stations.yml` is walked as an ordered `yaml.Node` tree so file order and
  duplicate handling stay identical to before;
- `golang.org/x/image/draw` (CatmullRom scaler) — replaces the hand-written
  box filter for icon resizing, with better output quality.

Selection criteria: widely adopted and maintained, few or no transitive
dependencies, pure Go (no cgo) so cross-compilation for Raspberry Pi / MIPS
routers keeps working, each addition justified by an entry here.

**Amendment**: dependencies are vendored (`go mod vendor`, `vendor/` in the
repository). Builds are network-independent — required for the Debian and
RPM packaging environments, where fetching modules at build time is neither
reproducible nor always possible.

**Why**: hand-rolling parsers and scalars was a correctness and maintenance
risk with no benefit once the byte-exact compatibility constraint was scoped
to the vTuner XML only (which stays hand-written, see #2). Standard-library-
only code reads like a stunt, not like state-of-the-art Go.

**Consequences**: the binary is no longer stdlib-only (still a small, static,
cross-compilable binary); `go.sum` must be kept tidy (`go mod tidy` in CI
habits); the parser now accepts the full YAML feature set rather than a
documented subset.

---
*Format loosely inspired by Architecture Decision Records (ADRs), lightweight
version: one decision = a title, what is decided, why, and the accepted
consequences.*

## 12. Standard cmd/ + internal/ project layout

**Decision**: the single `package main` at the repository root is split into
the conventional Go project layout:

- `cmd/ycast-go/` — entry point only (flags, boot, graceful shutdown);
- `internal/station/` — the shared `Station` type and the ID scheme;
- `internal/vtuner/` — the vTuner XML generation (see #2);
- `internal/mystations/` — stations.yml source with hot reload;
- `internal/radiobrowser/` — the Community Radio Browser client;
- `internal/icons/` — icon download, conversion and disk cache;
- `internal/server/` — HTTP routing, handlers and the stream relay.

Tests moved next to the code they cover (internal test packages). The
previously unexported types crossing package boundaries are now exported
(`station.Station`, `vtuner.Page`, `radiobrowser.Client`, `icons.Cache`,
`server.Config`...).

**Why**: at ~2,000 lines in one package the boundaries between subsystems
were invisible, everything could reference everything, and the code could not
be imported or tested in isolation. `internal/` keeps the API surface private
while enforcing the architecture through imports.

**Consequences**: build commands target `./cmd/ycast-go` (updated in
`build.sh`, README and AGENTS.md); no behavioural change — the full test
suite, including the byte-for-byte golden tests, passes unchanged.

## 13. Native packaging sources (RPM spec + debian/), built by CI

**Decision**: distribution packages come from native packaging sources kept in
the repository:

- `packaging/rpm/ycast-go.spec` — standard RPM spec, config as
  `%config(noreplace)`, systemd scriptlets (`%systemd_post` & co), builds
  with `rpmbuild` from a versioned source tarball;
- `packaging/deb/debian/` — standard debhelper 13 tree, config as conffile,
  service installed but not enabled/started (the admin must configure
  stations.yml and the DNS redirection first), version injected from
  `debian/changelog`.

Both dirs carry a README with the exact build commands. GitHub Actions will
perform the actual builds (a Debian container for the deb, a Fedora runner or
container for the rpm).

**Why**: native packaging sources are reviewable, distro-conventional and do
not depend on a third-party packaging tool (an nfpm-based attempt was
discarded: its config was less readable, its env expansion in globs
idiosyncratic and it would have hidden the spec/metadata from distro
reviewers). Keeping sources here and building in CI follows the common
open-source pattern.

**Consequences**: package versions live in two places (`Version:` in the
spec, `debian/changelog`) that CI must keep in sync with the release version;
the deb is built from the source tree (native format), not from a Debian
orig tarball.

## 14. Official Docker image on debian:trixie-slim

**Decision**: an official multi-arch image (`amd64`/`arm64`) is published to
GHCR at each release. Multi-stage `Dockerfile`: build stage on
`debian:trixie-slim` with `golang-any` and the vendored dependencies (no
network fetch of modules), runtime stage on the same base with the binary, a
default `/etc/ycast/stations.yml`, an unprivileged `ycast` user, port 8080
(the receiver's port 80 is mapped by the user), a cache volume and a
`/healthz`-based HEALTHCHECK using bash's `/dev/tcp` (slim ships no curl).

**Why**: `trixie-slim` matches the Debian package's target distribution and
keeps the image small; one base for build and runtime keeps the supply chain
to a single, auditable distro. Running unprivileged with a mapped port is the
usual container trade-off (port 80 requires CAP_NET_BIND_SERVICE otherwise).

**Consequences**: the image rebuilds the binary (no cross-compiled artifact
reuse — acceptable, the build is seconds and buildx builds per-architecture
natively through QEMU); `deploy/docker-compose.yml` documents the volume and
port mapping; the version string is injected through a `VERSION` build arg.

**Amendment**: base images are date-pinned for idempotent builds —
`debian:trixie-20261005-slim` in the Dockerfile, `debian:trixie-20261005` for
the deb CI job, `fedora:44` for the rpm job. Bumping the pin is a deliberate,
reviewable change.

## 15. OpenWrt packages from the official SDK (ipk for 24.10, apk for 25.12)

**Decision**: OpenWrt packages come from a native buildroot package Makefile
(`packaging/openwrt/`), built in CI with the official OpenWrt SDKs, in a
four-way matrix: release 24.10.8 (`opkg`, `.ipk`) and current snapshot
(`apk-tools`, `.apk`) × `armsr/armv8` (`aarch64_generic`) and `armsr/armv7`
(`arm_cortex-a15_neon-vfpv4`). The Go build goes through the packages feed's
`golang-package.mk`; the package pulls the sources by git at a pinned commit
SHA and installs the binary, a procd init script and
`/etc/ycast/stations.yml.example`; `DEPENDS:=+ca-bundle` for the HTTPS trust
store. The 24.10.8 SDK is pinned; the snapshot SDK cannot be pinned (it
changes daily), so its sha256 is verified against upstream's published
`sha256sums` at download time.

**Why**: users asked for OpenWrt packages (`apk` for arm32/arm64). OpenWrt
24.10 still ships opkg — a `.apk` only installs on the apk-based generation —
so both formats are produced. Building inside the official SDK (instead of
hand-crafting package files) is what guarantees installable metadata
(architecture, dependencies, scripts), matches decision #13's "native
packaging sources" philosophy and lets users rebuild for any of OpenWrt's
other ARM sub-architectures with one documented command. A static Go binary
runs on any ARMv7, but the package architecture is what the installer checks,
so one arm32 arch had to be chosen (the generic `armsr/armv7` one).

**Consequences**: CI gains a slow matrix job (the `golang/host` toolchain is
built from source in each SDK, several minutes per job); the snapshot `.apk`
files are unsigned
(`apk add --allow-untrusted`); the arm32 package only installs as-is on
architectures matching `arm_cortex-a15_neon-vfpv4` — documented in
`packaging/openwrt/README.md`. None of this has been tested on real
hardware; the README says so.

**Amendment**: with the OpenWrt 25.12 release out, the two snapshot jobs are
replaced by pinned 25.12.5 SDK jobs (same architectures
`aarch64_generic`/`arm_cortex-a15_neon-vfpv4`, same `.apk` format); the
"snapshot tracks a moving upstream" caveat is gone — all four SDKs are now
version-pinned with sha256 verification.

## 16. golangci-lint: curated config, not maximal strictness

**Decision**: `golangci-lint` (v2) joins the project bar (`go vet`, `gofmt`,
`go test -race`) with a curated `.golangci.yml`, run in CI from the pinned
official binary (no third-party action). The linter set keeps the correctness
and hygiene checks (govet, staticcheck, errcheck, errchkjson, gocritic,
bodyclose, noctx, revive exported-comments, intrange, prealloc, misspell…).
The contributed config was visibly imported from another codebase (a foreign
company's internal GitLab module prefix in `gci`, gRPC ignores in
`wrapcheck`, SQL/OTel linters); it was re-curated, and several linters were
disabled on purpose: `sloglint`'s `attr-only` (the codebase logs with the
idiomatic key-value form — 29 call sites), `wrapcheck` and `err113` (they
forbid idiomatic `fmt.Errorf` and conflict with the documented
graceful-degradation style), `funcorder`/`gocognit`/`maintidx` (cosmetic
churn), `gocyclo` is kept as a guardrail at 25 with tests excluded.

**Why**: a linter is a tool in service of the project's own conventions
(AGENTS.md), not a license to rewrite the codebase in the linter's taste. The
disabled checks had no correctness value here and would have produced ~85
style-only changes (rewriting every log call, wrapping every stdlib error,
re-ordering methods), increasing the diff and the risk on a codebase with
byte-for-byte compatibility constraints. The ~45 real findings (response-body
leaks in tests, `os.Exit` skipping deferred calls in `main`, missing API doc
comments, `http.NoBody`, unchecked errors, integer ranges, preallocation)
were all fixed instead.

**Consequences**: CI runs golangci-lint (checksum-pinned binary, version
following `mise.toml`); `.golangci.yml` documents each deviation at the top;
`AGENTS.md` lists the new command. Any future "strictness" request must
justify itself against this entry.

## 17. golangci-lint: slimmed down to the essential set (amends #16)

**Decision**: the curated config from #16 is replaced by a deliberately lean
one: `errcheck` (with the same deliberate ignores), `govet`, `staticcheck`
(which subsumes the removed-in-v2 `gosimple`/`stylecheck`), `ineffassign`,
`unused`, `misspell`, `gosec` (with documented exclusions: G401/G501 for the
required MD5 station IDs, G404 for the non-crypto `math/rand` shuffle, G104
as errcheck's job, G705/G710 taint false positives on the XML generator and
stream redirects), plus `bodyclose` and `noctx` — the two extra linters that
caught real leaks in the tests. Removed: `gocritic`, `revive`, `intrange`,
`prealloc`, `nilerr`, `nolintlint`, `containedctx`, `contextcheck`,
`errchkjson`, `errname`, `gocyclo`, `copyloopvar`, `bidichk`, `asciicheck`,
`nakedret`, `nilnil`, `unparam` and the formatters section (CI already runs
`gofmt`; the gofumpt fixes stay applied). The fixes made under #16 stay in
the code.

**Why**: the maintainer found #16's set still too aggressive. The value of a
linter set is not the number of linters but the findings; the two kept
specialists (`bodyclose`, `noctx`) are the only ones that found defects the
other checks cannot see. `gosec` joins because the project has genuine
security surfaces (station IDs in file paths, an HTTP relay); its exclusions
are each justified by a documented decision (the MD5 IDs, the XML generator).

**Consequences**: `.golangci.yml` is the lean config with the rationale
inlined; CI is unchanged (same pinned binary, same command). A rejected
finding should default to a code fix or a targeted, explained `//nolint`
(such as the icon-cache path read) rather than growing the exclusion list.

## 18. Plumber: exact pin, enforced in CI by the official action

**Decision**: plumber (the CI/CD security-policy analyzer behind
`.plumber.yaml`) is pinned to an exact version in `mise.toml` — never
`latest` — and the pin is updated manually with `mise run update-plumber`
(`scripts/update-plumber.sh`: reads the latest stable GitHub release, never
downgrades, rewrites the pin). The analysis itself runs in CI
(`.github/workflows/plumber.yml`) through the **official**
`getplumber/plumber` GitHub Action, SHA-pinned to the release matching the
mise.toml pin, with the installed binary verified against `checksums.txt`
and its SLSA build-provenance attestation by the action. Findings are
uploaded to GitHub Code Scanning (SARIF), CI is gated at `min-points: 100`
(any finding fails the job), and `score-push: true` publishes the live
Plumber Score badge shown in the README. The job is a **required status
check** on `main` alongside the CI checks, so a pull request that degrades
the score cannot be merged.

This is the **one deliberate third-party action** in an otherwise
GitHub-owned-only setup: it is allowed explicitly in the
`githubActionMustComeFromAuthorizedSources` overlay in `.plumber.yaml`, so
the policy tool polices its own dependency. An earlier weekly scheduled
workflow that auto-bumped the pin was removed on purpose: a pin bump must
be a reviewed, deliberate change.

**Why**: a policy tool is part of the supply chain: a floating `latest`
would make builds non-reproducible and let a plumber release change the
security gate silently. The pin gives reproducibility; running the scan on
every push/PR (not on a weekly schedule) means a policy regression is
detected in the change that introduced it, not days later. The 100-points
gate plus the required status check make the score monotonic: a merge
cannot lower it, and a fix is needed to raise it. The badge is published
via CI-native OIDC (`id-token: write`), no secret involved. The cost of
the strict gate — a new plumber release surfacing a new finding breaks CI
— is deliberate: it fails loudly, and the version bump is validated
manually before landing anyway.

**Consequences**: the pin can drift from upstream between manual updates;
that is acceptable for a security gate that should only move on purpose.
When bumping the pin, both the mise.toml pin and the action SHA (plus its
trailing version comment) move together, and the policy overlay must be
validated with the new version (`plumber config validate` + `plumber
analyze`) before landing; if a new plumber version rejects the policy
overlay, local validation fails loudly instead of the change landing.
