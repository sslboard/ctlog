# ctlog

List every hostname seen in Certificate Transparency logs for a domain, from the command line. Built by [SSLBoard](https://sslboard.com) as a fast, reliable alternative to crt.sh for subdomain recon.

```bash
ctlog example.com > hostnames.txt
```

## Install

```bash
go install github.com/sslboard/ctlog@latest
```

Prebuilt binaries for Linux, macOS and Windows (amd64 and arm64) are on the [releases page](https://github.com/sslboard/ctlog/releases).

## Usage

```bash
ctlog example.com                      # text, one hostname per line
ctlog %.example.com                    # same thing, crt.sh habit accepted
ctlog example.com -o json              # SSLBoard JSON response
ctlog example.com -o crtsh             # crt.sh-shaped JSON for drop-in scripts
ctlog example.com --no-wildcards       # drop *.example.com style entries
ctlog example.com --no-apex            # exclude the queried name itself
ctlog version
```

`ctlog find <domain>` is an alias of `ctlog <domain>`.

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
- Results are ordered by reversed labels (`com.example.api` sorts before `com.example.www`), so whole subtrees stay together.

## Limits

Without an account, each search returns up to 100 hostnames and you can run 1 search per minute. When a domain has more, ctlog says so on stderr and links to the full interactive map:

```
100 of 12,432 hostnames. Browse the full map: https://sslboard.com/subdomain/example.com
```

Higher limits with an API key are planned.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success, including zero results |
| 1 | Usage error or invalid domain |
| 2 | API or network error |
| 3 | Rate limited (ctlog never waits or retries on its own) |

## Environment

| Variable | Description |
| --- | --- |
| `CTLOG_API_URL` | API base URL, default `https://sslboard.com`. Useful for testing against `wrangler dev`. |

## API

ctlog is a thin client for `GET https://sslboard.com/api/v1/hostnames?q=example.com`, which you can also call with curl (`&format=text` for plain lines).

## Development

```bash
go test ./...
go build -o bin/ctlog .
CTLOG_API_URL=http://localhost:8787 ./bin/ctlog example.com
```

Releases are cut by pushing a `v*` tag; GitHub Actions runs GoReleaser.

## License

MIT, see [LICENSE](LICENSE).
