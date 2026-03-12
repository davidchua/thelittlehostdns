The Little Host for [`libdns`](https://github.com/libdns/libdns)
==================================================================

[![Go Reference](https://pkg.go.dev/badge/github.com/libdns/thelittlehost.svg)](https://pkg.go.dev/github.com/libdns/thelittlehost)

This package implements the [libdns interfaces](https://github.com/libdns/libdns) for [The Little Host](https://thelittlehost.com), allowing you to manage DNS records programmatically.

## Configuration

```go
provider := &thelittlehost.Provider{
    APIToken: "tlh_your_api_token_here",
}
```

### Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `APIToken` | `string` | **Yes** | Bearer token for API authentication. Generate one in the The Little Host control panel. Tokens are prefixed with `tlh_`. |

### Environment variables

This package does not read environment variables directly. Set struct fields explicitly, or use environment variables as defaults in your application:

```go
provider := &thelittlehost.Provider{
    APIToken: os.Getenv("THELITTLEHOST_API_TOKEN"),
}
```

## Usage with CertMagic / Caddy

```go
import "github.com/libdns/thelittlehost"

provider := &thelittlehost.Provider{
    APIToken: "tlh_...",
}
```

In Caddy JSON config:

```json
{
  "module": "thelittlehost",
  "api_token": "tlh_..."
}
```

## Caveats

- **MX records**: Priority is encoded in the `Data` field as `"<preference> <target>"` per the libdns convention and is split out for the API automatically.
- **DeleteRecords wildcards**: Empty `Type`, `TTL`, or `Data` fields on an input record act as wildcards — any existing record matching the non-empty fields will be deleted.
- **Concurrency**: All methods are safe for concurrent use.

## License

MIT — see [LICENSE](LICENSE).
