# Releasing ycast-go

How releases are produced: everything is automated by GitHub Actions, the
only human gesture is pushing a tag. This file documents the full flow, the
artifacts, and the recovery procedures.

## The pipeline at a glance

`.github/workflows/release.yml` triggers on pushing a tag matching `v*`.
The version is derived from the tag (`v1.0.0` → `1.0.0`) and injected into
every artifact (binary via `-ldflags`, deb via `debian/changelog`, rpm via
the spec `Version:` field).

```
git push origin v1.0.0
        │
        ├─ job binaries      → ./build.sh → 6 binaries (amd64, arm64, armv7, armv6, mips, mipsle)
        ├─ job deb           → debian:trixie-20261005 container → ycast-go_1.0.0_amd64.deb
        ├─ job rpm           → fedora:44 container              → ycast-go-1.0.0-1.fc44.x86_64.rpm
        ├─ job openwrt       → OpenWrt SDK matrix (4 jobs)      → ycast-go .ipk/.apk (arm64, arm32)
        ├─ docker-amd64      → native build (ubuntu-latest) → push :sha-xxxx-amd64@digest (GHCR)
        ├─ docker-arm64      → native build (ubuntu-24.04-arm) → push :sha-xxxx-arm64@digest (GHCR)
        ├─ docker-manifest   → merge → ghcr.io/hgomez/ycast-go:{1.0.0, 1.0, latest}
        └─ job release       → gh release create --generate-notes + upload of everything
```

All jobs run in parallel except `docker-manifest` (waits for its two builds)
and `release` (waits for `binaries`, `deb`, `rpm` and `openwrt`). A failure
in one job does not block the others. The `openwrt` matrix builds four
packages (release 24.10.8 `.ipk` and release 25.12.5 `.apk`, for `aarch64_generic`
and `arm_cortex-a15_neon-vfpv4` — see `packaging/openwrt/README.md`); it is
the slowest job (each SDK builds the Go toolchain from source, ~10 min).

## Step by step

1. **Check CI is green on `main`** — branch protection requires it for PRs,
   and a release built from a red `main` is a bad idea anyway:
   ```sh
   gh run list --branch main --limit 3
   ```
2. **Check the indicative version in `main.go`** (`var version = "…"`) — purely
   cosmetic: artifacts are stamped from the tag; the `main.go` value only
   matters for builds without `-ldflags` (e.g. plain `go install`).
3. **Create the tag and the release**:
   ```sh
   git tag -a v1.0.0 -m "1.0.0" && git push origin v1.0.0
   # or in one step, creating both on GitHub:
   gh release create v1.0.0 --title v1.0.0 --generate-notes
   ```
4. **Watch the run**: https://github.com/hgomez/ycast-go/actions — expect
   five to fifteen minutes end to end (the packaging containers and the
   OpenWrt SDK jobs are the slow part).
5. **Verify**:
   - https://github.com/hgomez/ycast-go/releases shows the release with the
     6 binaries, the `.deb`, the `.rpm` and the 4 OpenWrt packages;
   - `podman pull ghcr.io/hgomez/ycast-go:1.0.0` works;
   - sanity-check an artifact locally:
     `qemu-aarch64 ./ycast-go-linux-arm64 -version` → `ycast-go 1.0.0`.

## First release: GHCR package visibility

On the first push, the `ghcr.io/hgomez/ycast-go` package is created and
linked to the repository. It should inherit the repo visibility (public), but
check https://github.com/hgomez?tab=packages once: if it shows *Private*,
switch it to *Public* under *Package settings*. This is a one-time step.

## Version sources: what you maintain and what you don't

| Where | Who sets it | Notes |
|---|---|---|
| git tag | **you**, the trigger | single source of truth for all artifacts |
| `main.go` `var version` | you, cosmetic | fallback for builds without `-ldflags` |
| `debian/changelog` | CI (`sed` before `dpkg-buildpackage`) | commit the change back if you care |
| rpm spec `Version:` | CI (`sed` before `rpmbuild`) | same |

A future improvement would be a `prepare-release` workflow opening a PR that
bumps all of them from the tag; today the CI-side `sed` makes the manual
burden limited to `main.go`.

## Recovery procedures

A release went wrong? Everything is reversible:

```sh
gh release delete v1.0.0 --yes          # remove the GitHub release
git push origin :refs/tags/v1.0.0       # remove the remote tag
git tag -d v1.0.0                       # remove the local tag
# fix what needs fixing, then push the tag again — the pipeline re-runs
```

The `release` job itself is idempotent (`gh release upload --clobber` and a
conditional `gh release create`), so re-running over an existing release
merely refreshes the artifacts. The Docker manifest merge is not idempotent
by digest (`:1.0.0` is overwritten), which is fine: it always points at the
latest push of that tag.

## Security posture of the pipeline

Enforced by the `.plumber.yaml` policy (score 100/100 at the time of
writing) and `AGENTS.md`:

- only GitHub-owned actions (`actions/*`), each pinned to a commit SHA;
- images pinned by digest (manifest-list digests, valid for multi-arch);
- tools in `mise.toml` are pinned to exact versions; the plumber pin is
  moved weekly by `.github/workflows/plumber.yml` (PR with the new pin,
  policy re-validated with the new version before the PR opens);
- OpenWrt SDKs pinned by version (24.10.8 for the `.ipk` jobs, 25.12.5 for
  the `.apk` jobs), each download verified over HTTPS against upstream's
  published `sha256sums`;
- no third-party release action — publishing uses the preinstalled `gh` CLI;
- `persist-credentials: false` on every checkout;
- per-job `permissions:` (least privilege);
- `main` is branch-protected (force-push and deletion forbidden, CI checks
  required on PRs);
- dependencies are vendored, so the deb/rpm builds fetch nothing from the
  network except their own base image packages.

## Roadmap (not implemented)

- `workflow_dispatch` trigger with a version input, for ad-hoc binaries
  without tagging (mind the git→artifact traceability);
- draft releases by default (`--draft` in the `release` job) for a human
  review gate before publishing;
- SLSA build provenance attestations
  (`actions/attest-build-provenance`) on every artifact;
- a `prepare-release` workflow (see above).
