---
title: Install
section: Start
order: 20
summary: Install Harness from a release binary or from source.
---

Harness is a single static binary. Pick whichever route is easiest.

## From a release

Every release publishes binaries for five targets on the [releases page](https://github.com/gurcuff91/harness/releases), with a `SHA256SUMS.txt` to verify them:

| Platform | Asset |
|---|---|
| macOS (Apple Silicon) | `harness-<version>-darwin-arm64.tar.gz` |
| macOS (Intel) | `harness-<version>-darwin-amd64.tar.gz` |
| Linux (x86-64) | `harness-<version>-linux-amd64.tar.gz` |
| Linux (ARM64) | `harness-<version>-linux-arm64.tar.gz` |
| Windows (x86-64) | `harness-<version>-windows-amd64.zip` |

```bash
# example: macOS on Apple Silicon
$ tar xzf harness-<version>-darwin-arm64.tar.gz
$ shasum -a 256 -c SHA256SUMS.txt --ignore-missing
$ mv harness ~/.local/bin/
```

## From source

You need Go 1.25 or newer.

```bash
$ go install github.com/gurcuff91/harness/cmd/harness@latest
```

Or clone the repository and use the Makefile, which injects the version into the binary:

```bash
$ git clone https://github.com/gurcuff91/harness
$ cd harness
$ make install        # builds and copies the binary to ~/go/bin
```

> **Tip** Make sure `~/go/bin` (or wherever you put the binary) is on your `PATH`.

## Check it works

```bash
$ harness --version
```

If that prints a version, you are ready to [connect a provider](connect-a-provider.md).

## Where Harness keeps its data

Everything lives under `~/.harness/`: your credentials (file mode `0600`), your settings, sessions and memory. Nothing is sent anywhere except to the providers you connect. See [Settings and data](../reference/settings.md) for the full layout.
