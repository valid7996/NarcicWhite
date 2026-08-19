# Narcic White — Desktop

A free, open-source desktop VPN client built with [Wails](https://wails.io)
(Go + React/TypeScript). It connects through [mihomo](https://github.com/MetaCubeX/mihomo),
manages subscriptions and manual server profiles, and mirrors the settings and
behavior of the Narcic White Android app so switching between phone and
computer feels the same. The interface supports both Persian and English.

> برنامه‌ی دسکتاپ Narcic White، یک کلاینت VPN رایگان و متن‌باز است که تنظیمات
> و رفتار آن با نسخه‌ی اندروید هماهنگ شده. راهنمای نصب و دانلود فارسی را در
> [`desktop/DOWNLOAD_GUIDE_FA.md`](desktop/DOWNLOAD_GUIDE_FA.md) ببینید.

## Features

- One-click connect/disconnect, with live status, speed and data usage
- Subscription management (add, refresh, import profiles from a link) plus
  manual server entry
- An optional built-in catalogue subscription, encrypted and refreshed on a
  schedule — no address stored or shown until it's fetched
- A Servers page for scanning and measuring reachability/delay across nodes
- Split tunneling (by process, on desktop) and configurable DNS/resolver
  behavior
- System tray integration and local proxy support for use in browsers and
  other apps
- Windows, macOS, and Linux builds, packaged as a single binary with the
  engine embedded

See [`desktop/ANDROID-PARITY.md`](desktop/ANDROID-PARITY.md) for the exact
setting-by-setting mapping to the Android app.

## Installing

Download a release for your platform from the
[Releases page](https://github.com/YOUR-ORG/narcic-white/releases), or read
the [quick download guide](desktop/DOWNLOAD_GUIDE_FA.md) (Persian) if you're
not sure which file you need.

## Building from source

Requirements:

- [Go](https://go.dev) 1.25+
- [Node.js](https://nodejs.org) and npm
- The [Wails CLI](https://wails.io/docs/gettingstarted/installation) (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`)
- On Linux: `libwebkit2gtk` (see below for Ubuntu 24.04+)

From the repository root:

```bash
make deps    # npm install + go mod tidy
make dev     # run in dev mode
make build   # build the frontend and compile the desktop binary
make test    # run Go tests and the frontend build
```

Platform-specific packaging:

```bash
make package-windows
make package-mac                 # must run on a Mac
make package-linux               # must run on Linux
make package-linux-distros       # .deb and .rpm from a Linux build
make package-linux-all-docker    # Linux packages from a non-Linux host
```

Pass a version with `VERSION=1.0.0`, or `make all -- --version 1.0.0`.
Linux packaging needs `dpkg-deb` and `rpmbuild`; for an AppImage, install
`linuxdeploy` or set `LINUXDEPLOY_BIN`. On Ubuntu 24.04+, pass
`LINUX_GO_TAGS=webkit2_41` and install `libwebkit2gtk-4.1-dev`.

The engine itself is not vendored — `make mihomo-core` fetches and builds it
from its pinned upstream ref. See
[`desktop/README.md`](desktop/README.md) for engine details, developer notes,
and two build traps worth knowing about before they cost you an afternoon.

## Repository layout

```
desktop/          The Wails app: Go backend + React/TypeScript frontend
TunnelCheck/       A separate module used to score/validate tunnel endpoints
packaging/        Platform packaging assets
```

## Security

Found a vulnerability? See [SECURITY.md](SECURITY.md) for the reporting
process and what's in scope.

## License

GPL-3.0-or-later.
