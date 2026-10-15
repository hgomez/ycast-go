# YCast (Go edition)

[![Plumber Score](https://score.getplumber.io/github.com/hgomez/ycast-go.svg)](https://score.getplumber.io/github.com/hgomez/ycast-go)

Self-hosted replacement for the **vTuner** internet radio service used by many
network receivers (Denon, Marantz, Yamaha, Onkyo…). It puts your own stations
in the receiver's "Internet Radio" menu, so **the front-panel controls work
on them**.

A Go port of [YCast](https://github.com/milaq/YCast) by milaq, GPLv3.
One small static binary, no cgo, cross-compilable for Raspberry Pi, NAS boxes
or routers. Dependencies are kept to a strict minimum (`gopkg.in/yaml.v3`,
`golang.org/x/image`) — see `docs/DECISIONS.md`.

## Setup (example: Denon DRA-N5)

1. **Pick the machine** that will run the server (Pi, NAS, mini PC, router…).
   It needs a fixed IP and a **free port 80** (receivers only talk to port 80).
2. **Copy the matching binary** from `dist/` (see below). Or build it:
   `go build -trimpath -ldflags "-s -w" -o ycast-go ./cmd/ycast-go`

   ### Building / cross-compiling

   The code is pure Go (no cgo), so everything cross-compiles from any host
   with the Go toolchain — no cross-compiler or sysroot needed:

   ```sh
   ./build.sh              # fills dist/ for amd64, arm64, armv7, armv6, mips, mipsle
   ./build.sh 1.2.3        # same, stamping the version string
   ```

   Manual equivalent for one target:

   ```sh
   # ARM 64-bit (Pi 3/4/5, ARM servers):
   CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
     go build -trimpath -ldflags "-s -w" -o dist/ycast-go-linux-arm64 ./cmd/ycast-go
   # ARM 32-bit: GOARM=7 for Pi running armhf, GOARM=6 for Pi Zero/1.
   CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
     go build -trimpath -ldflags "-s -w" -o dist/ycast-go-linux-armv7 ./cmd/ycast-go
   ```

   `GOARM=6` runs on every ARM board; try `armv7` first on a 32-bit OS and
   fall back to `armv6` on "Illegal instruction". To sanity-check a binary
   without the hardware: `dnf install qemu-user && qemu-aarch64
   dist/ycast-go-linux-arm64 -version`.
3. **Create your `stations.yml`** (see `stations.yml.example`).
4. **Run it**: `sudo ./ycast-go -c stations.yml` (add `-d` for verbose logs).
5. **Redirect DNS**: `denon.vtuner.com` and `denon2.vtuner.com` must point to
   the server's IP (see below). For Yamaha/Onkyo/Grundig, see the YCast README.
6. **Check** from a PC: `curl -H 'Host: denon.vtuner.com' http://SERVER_IP/ycast/`
   must return XML containing "Radiobrowser" and "My Stations".
7. **Reboot the receiver** (or exit/re-enter the Internet Radio source) and
   open the menu. If the receiver has a fixed IP, make sure **no** other DNS
   than the one doing the redirection is configured on it.

### DNS redirection

- **OpenWrt / dnsmasq** (replace the IP with the server's):
  ```sh
  uci add_list dhcp.@dnsmasq[0].address='/denon.vtuner.com/192.168.1.10'
  uci add_list dhcp.@dnsmasq[0].address='/denon2.vtuner.com/192.168.1.10'
  uci commit dhcp && /etc/init.d/dnsmasq restart
  ```
- **Pi-hole**: *Local DNS → DNS Records*, two A records.
- **Router without local DNS**: point the receiver at a DNS that does the
  redirection (Pi-hole, dnsmasq…).
- Test: `nslookup denon.vtuner.com` from a device on the network must return
  the server's IP.

### Starting at boot

- **systemd**: `deploy/ycast-go.service` (binary in `/usr/local/bin`, config
  in `/etc/ycast/`).
- **OpenWrt**: the official packages from the GitHub release are the easiest
  path — they ship the binary, `/etc/ycast/stations.yml.example` and a procd
  init script:
  ```sh
  # OpenWrt 24.10 (opkg, .ipk) — arm64 here, see below for arm32:
  opkg install ycast-go_1.0.0-1_aarch64_generic.ipk
  # OpenWrt 25.x (apk, .apk) — the package is unsigned:
  apk add --allow-untrusted ycast-go-1.0.0-1.apk
  cp /etc/ycast/stations.yml.example /etc/ycast/stations.yml  # then edit it
  /etc/init.d/ycast enable && /etc/init.d/ycast start
  ```
  The package declares a single ARM 32-bit architecture
  (`arm_cortex-a15_neon-vfpv4`, generic `armsr/armv7` target); the binary is
  static ARMv7, but the installer checks the declared architecture. Other
  ARM sub-archs (cortex-a7, cortex-a9…) and other targets rebuild from the
  same sources with the matching SDK — see `packaging/openwrt/README.md`.
  LuCI (uhttpd) occupies port 80 by default, move it first:
  `uci set uhttpd.main.listen_http='0.0.0.0:8080'; uci commit uhttpd; /etc/init.d/uhttpd restart`
  (the web interface then lives on port 8080). The OpenWrt package and the
  manual install (`deploy/openwrt-ycast.init`) have not been tested on real
  hardware.
- Port 80 without root: `sudo setcap 'cap_net_bind_service=+ep' ./ycast-go`.
- **Docker/Podman** (`deploy/docker-compose.yml` for Compose):
  ```sh
  docker run -d -p 80:8080 \
    -v ./stations.yml:/etc/ycast/stations.yml:ro \
    ghcr.io/hgomez/ycast-go:latest
  ```
  Multi-arch image (`amd64`/`arm64`), built on `debian:trixie-slim`, runs as
  an unprivileged user on port 8080; map the receiver's port 80 to it. The
  config is reloaded automatically, so editing the mounted `stations.yml` is
  enough.

## stations.yml

```yaml
Chillout:
  SomaFM Groove Salad: http://ice1.somafm.com/groovesalad-256-mp3
  "SomaFM: Lush": http://ice1.somafm.com/lush-128-mp3   # names containing ": " need quotes
France:
  FIP: https://icecast.radiofrance.fr/fip-midfi.mp3     # HTTPS is fine (relayed)
```

The file is re-read automatically when it changes (no restart needed). If an
edit is invalid, the previous version keeps being served and the error is
logged. Full YAML is supported (parsed with `gopkg.in/yaml.v3`); categories
and their stations keep their file order, duplicate names: last one wins,
keeps its position.

Station IDs are computed exactly like in YCast Python: **presets already
stored on the receiver remain valid** after migrating.

## Options

| Option | Default | Purpose |
|---|---|---|
| `-c` | – | `stations.yml` file |
| `-l` / `-p` | `0.0.0.0` / `80` | listen address and port |
| `-d` | off | debug logging: every incoming request (full URI, source IP) and the XML response sent to the receiver |
| `-log-json` | off | log in JSON instead of plain text (for log collectors such as Loki/ELK) |
| `-proxy-https` | `true` | relay HTTPS streams (receivers cannot do TLS); `false` = downgrade to HTTP like YCast Python |
| `-max-stations` | `500` | stations listed per country/language/genre (most voted first, sorted by name) |
| `-cache` | user cache dir | icon cache directory (`none` disables caching) |
| `-rb-api` | DNS discovery | Radio Browser API base URL |
| `-version` | | print version and exit |

`GET /healthz` answers `ok` (for monitoring).

## Differences from YCast Python

- **HTTPS relay** (`/ycast/stream`): HTTPS-only stations work. Only known
  stations (by ID) are relayed, never arbitrary URLs.
- Hot reload of `stations.yml`; search also covers "My Stations".
- Radio Browser: servers discovered through DNS, HTTPS, 5-minute cache, list
  size capped for small machines.
- No more Python, Flask, Pillow…: one small Go binary with two vetted
  dependencies (`gopkg.in/yaml.v3`, `golang.org/x/image`); the vTuner XML
  stays hand-written for byte-for-byte compatibility.

Not implemented: favourites (broken in the original too), server-side
`.m3u`/`.pls` playlists, upstream "ICY 200 OK" (Shoutcast v1) streams when
relayed.

## Tests

`go test -race ./...`: 24 tests. The XML and the station IDs are compared byte
for byte against the output of the original Python code. Radio Browser is
simulated; the HTTPS relay is tested against a fake TLS server.
**Never tried on a real Denon or against the real radio-browser.info** (no
network access in the development environment): validate on your hardware and
turn on `-d` if something misbehaves.

## Packaging

Native packaging sources for Linux distributions (built by CI):

- `packaging/rpm/` — RPM spec (`rpmbuild -bb`, tested on Fedora). The config
  ships as `%config(noreplace)`.
- `packaging/deb/` — debhelper `debian/` tree (`dpkg-buildpackage -b`). The
  config ships as a conffile; the service is installed **not enabled**: the
  admin must edit `/etc/ycast/stations.yml` and set up the DNS redirection
  first.

Each directory has a README with the exact build commands.

## Contributing

`AGENTS.md` (rules for agents and contributors: curated dependencies, byte-for-
byte compatibility, tests) and `docs/DECISIONS.md` (design decision log)
document the project's constraints; read them before changing anything.

## CI / Release

- `.github/workflows/ci.yml` — on every push/PR: gofmt, `go vet`,
  `go test -race`, cross-compile smoke test (arm64 + armv7).
- `.github/workflows/release.yml` — on tag `v*`: all cross-compiled binaries
  (`build.sh`), the Debian package (in a digest-pinned `debian:trixie-20261005`
  container), the RPM package (in a `fedora:44` container), a multi-arch
  Docker image (`amd64`/`arm64`, native per-arch runners) pushed to GHCR, all
  published on the GitHub release.
- **`RELEASING.md`** — the full release procedure: what a tag produces, the
  verification steps, recovery procedures and the security posture of the
  pipeline.

## Licence

GPLv3 or later, like YCast (see `LICENSE`).
