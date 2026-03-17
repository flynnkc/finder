# Finder – OCI Structured Search CLI

## Overview

This command line tool issues an [Oracle Cloud Infrastructure](https://www.oracle.com/cloud/) Structured Search query for a specific resource type/identifier combination. Supply the OCI region, CLI profile, resource type, and resource identifier via flags.

## Prerequisites

- Go 1.21+
- OCI CLI-style credentials stored in `~/.oci/config` with at least one profile capable of calling the SearchService

## Installation

```bash
git clone git@github.com:flynnkc/finder.git
cd finder
go build ./cmd/finder
```

## Usage

```
./finder \\
  --resource-type instance \\  # optional, defaults to "all" \\
  --region us-phoenix-1 \\      # optional, defaults to the profile's region \\
  --profile DEFAULT \           # optional, defaults to DEFAULT \\
  --concurrency 5 \             # optional, concurrent worker count \\
  --rate-limit 5 \              # optional, tokens refilled per interval
  ocid1.instance.oc1.phx.<id1> \\
  ocid1.instance.oc1.phx.<id2>
```

### Flags

| Flag | Required | Description |
| ---- | -------- | ----------- |
| `--region` | No | OCI region (e.g., `us-phoenix-1`). Defaults to the profile’s region unless an OCID-derived region (including short codes like `phx`/`iad`) is detected automatically. |
| `--resource-type` | No | Resource type, e.g., `instance`, `bucket`, or `all` (default). |
| `resource-id` (positional) | Yes | One or more identifiers/OCIDs to search |
| `--profile` | No (default `DEFAULT`) | Profile from `~/.oci/config` |
| `--debug` | No | Emits verbose logging about region inference and API calls. |
| `--concurrency` | No (default `5`) | Maximum number of concurrent OCI search workers |
| `--rate-limit` | No (default `5`) | Number of requests replenished each `--rate-interval` |
| `--rate-interval` | No (default `1s`) | Interval governing token refills (e.g., `500ms`, `2s`) |
| `--rate-burst` | No (default `rate-limit`) | Maximum burst size; controls token pool capacity |

### Output

- The CLI prints a single JSON array. Each entry corresponds to one provided OCID and contains:
  - `ocid`: the original identifier
  - `region`: the region used for the query (either inferred or overridden via flag)
  - `resources`: any `ResourceSummary` objects returned for that OCID
  - `message`: present when no resources were returned or an error occurred (including rate-limit exhaustion)
- The order of entries matches the order of the provided OCIDs, even when requests are executed concurrently.

### Troubleshooting

- Ensure the configured profile has permissions to `SEARCH`. Missing permissions result in authorization errors from the OCI SDK.
- Verify the `identifier` field is correct for the resource type. Some resources may require alternate query fields.
- If the CLI infers the wrong region from the OCID, override it explicitly with `--region`. You can also run with `--debug` to inspect the inference process and see which tokens (including short codes) were evaluated.

## Development

Run lint/tests:

```bash
go test ./...
```

Ensure `gofmt` passes:

```bash
gofmt -w cmd/finder/main.go
```
