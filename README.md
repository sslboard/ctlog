# ctlog

[![Release](https://img.shields.io/github/v/release/sslboard/ctlog)](https://github.com/sslboard/ctlog/releases/latest)
[![Coverage](https://dev.gone-coding.com/api/project_badges/measure?project=sslboard_ctlog_b55a9bb3-ae07-48e1-aa98-9fe0e254bb29&metric=coverage&token=sqb_f606339d871ca65319eea93cd370fa9833e6bcb3)](https://dev.gone-coding.com/dashboard?id=sslboard_ctlog_b55a9bb3-ae07-48e1-aa98-9fe0e254bb29)
[![Quality gate status](https://dev.gone-coding.com/api/project_badges/measure?project=sslboard_ctlog_b55a9bb3-ae07-48e1-aa98-9fe0e254bb29&metric=alert_status&token=sqb_f606339d871ca65319eea93cd370fa9833e6bcb3)](https://dev.gone-coding.com/dashboard?id=sslboard_ctlog_b55a9bb3-ae07-48e1-aa98-9fe0e254bb29)

List every hostname seen in Certificate Transparency logs for a domain, from the command line. Built by [SSLBoard](https://sslboard.com) as a fast, reliable alternative to crt.sh for subdomain recon.

```bash
ctlog example.com > hostnames.txt
```

## Install

```bash
go install github.com/sslboard/ctlog@latest
```

On Linux and macOS, the install script fetches the latest release, verifies its checksum and installs it to `/usr/local/bin` (or `~/.local/bin`):

```bash
curl -fsSL https://raw.githubusercontent.com/sslboard/ctlog/main/install.sh | sh
```

`CTLOG_VERSION=v0.2.0` pins a version and `CTLOG_INSTALL_DIR` picks the directory. Prebuilt binaries for Linux, macOS and Windows (amd64 and arm64) are on the [releases page](https://github.com/sslboard/ctlog/releases).

## Usage

```bash
ctlog example.com                      # text, one hostname per line
ctlog %.example.com                    # same thing, crt.sh habit accepted
ctlog example.com -o json              # SSLBoard JSON response
ctlog example.com -o crtsh             # crt.sh-shaped JSON for drop-in scripts
ctlog example.com --no-wildcards       # drop *.example.com style entries
ctlog example.com --no-apex            # exclude the queried name itself
ctlog bücher.example --unicode         # print IDNs as Unicode instead of punycode
ctlog example.com --all                # every page (hostname API plan)
ctlog -f domains.txt                   # one domain per line, results merged and deduplicated
ctlog login                            # sign in through the browser
ctlog login --key sslb_live_...        # save an existing key (--key - reads stdin)
ctlog status                           # account, plan, limits and usage
ctlog logout
ctlog version
```

`ctlog find <domain>` is an alias of `ctlog <domain>`. `-f -` reads the domain list from stdin; blank lines and `#` comments are skipped.

Hostnames go to stdout and notices go to stderr, so pipes only ever see hostnames. Use `-q` to silence notices.

### Migrating from crt.sh

```bash
# before
curl -s 'https://crt.sh/?q=%.example.com&output=json' | jq -r '.[].name_value' | sort -u

# after
ctlog example.com -o crtsh | jq -r '.[].name_value'
```

The `crtsh` format only fills `name_value` and `common_name` (one hostname per entry). Issuer, validity dates and certificate IDs are `null`: SSLBoard serves hostnames, not certificates.

## What the data is

- Hostnames from all currently valid certificates in the public CT logs, kept for 15 days after the certificate expires. There is no history of long-expired certificates.
- Wildcard names such as `*.example.com` are listed as they appear in certificates. Many sites have no certificate for `www.example.com` itself and rely on a wildcard instead. Use `--no-wildcards` to leave them out.
- Internationalized names are printed in punycode (`xn--bcher-kva.example`), the form certificates carry, so they work with DNS tools and match crt.sh output. Use `--unicode` to print `bücher.example` instead. You can type the domain in either form.
- Results are ordered by reversed labels (`com.example.api` sorts before `com.example.www`), so whole subtrees stay together.

## Limits and the hostname API plan

Without an account, each search returns up to 100 hostnames and you can run 1 search per minute. When a domain has more, ctlog says so on stderr:

```
100 of 12,432 hostnames (free limit). Run 'ctlog login' to get all of them: https://sslboard.com/docs/ctlog/#full-results
```

The hostname API plan ($9/month) returns every hostname, in pages of up to 10,000 that `--all` follows, at 60 requests per minute per key; `--all` and `-f` wait when they reach the limit instead of stopping.

`ctlog login` uses a device flow: it prints a code, opens `https://sslboard.com/device`, and waits while you approve it in the browser. The key is saved in `~/.config/ctlog/config.json` (`$XDG_CONFIG_HOME` is honoured, `%APPDATA%` on Windows) with 0600 permissions. `ctlog logout` deletes that file; revoke keys under [Account, API](https://sslboard.com/account/api/). A key whose plan has ended keeps working at the free limits.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success, including zero results |
| 1 | Usage error or invalid domain |
| 2 | API or network error |
| 3 | Rate limited (ctlog never waits or retries on its own) |
| 4 | API key rejected, or the browser login was denied or expired |

## Environment

| Variable | Description |
| --- | --- |
| `SSLBOARD_API_KEY` | API key for CI and servers. Takes precedence over the key saved by `ctlog login`. |
| `CTLOG_API_URL` | API base URL, default `https://sslboard.com`. Useful for testing against `wrangler dev`. |

## API

ctlog is a thin client for `GET https://sslboard.com/api/v1/hostnames?q=example.com`, which you can also call with curl (`&format=text` for plain lines). Send `Authorization: Bearer <key>` for paid limits and page with `after=<next>`. `GET /api/v1/me` backs `ctlog status`, and `POST /api/v1/device/code` plus `POST /api/v1/device/token` back `ctlog login`. The full reference is at [sslboard.com/docs/ctlog](https://sslboard.com/docs/ctlog/).

## Development

```bash
go test ./...
go build -o bin/ctlog .
CTLOG_API_URL=http://localhost:8787 ./bin/ctlog example.com
```

Releases are cut by pushing a `v*` tag; GitHub Actions runs GoReleaser. The Homebrew cask (`brew install sslboard/tap/ctlog`) is published to `sslboard/homebrew-tap` once that repository and the `HOMEBREW_TAP_GITHUB_TOKEN` secret exist; until then releases skip it.

## License

MIT, see [LICENSE](LICENSE).
