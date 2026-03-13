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
  --profile DEFAULT \           # optional, defaults to DEFAULT
  ocid1.instance.oc1.phx.<unique_ID>
```

### Flags

| Flag | Required | Description |
| ---- | -------- | ----------- |
| `--region` | No | OCI region (e.g., `us-phoenix-1`). Defaults to the profile’s region unless an OCID-derived region is detected automatically. |
| `--resource-type` | No | Resource type, e.g., `instance`, `bucket`, or `all` (default). |
| `--resource-id` | Yes | Identifier/OCID of the desired resource |
| `--profile` | No (default `DEFAULT`) | Profile from `~/.oci/config` |
| `--debug` | No | Emits verbose logging about region inference and API calls. |

### Output

- Successful queries print a pretty-formatted JSON array of matching resource summaries.
- If no matches are found, the CLI prints `No resources found matching the criteria.`

### Troubleshooting

- Ensure the configured profile has permissions to `SEARCH`. Missing permissions result in authorization errors from the OCI SDK.
- Verify the `identifier` field is correct for the resource type. Some resources may require alternate query fields.
- If the CLI infers the wrong region from the OCID, override it explicitly with `--region`. You can also run with `--debug` to inspect the inference process.

## Development

Run lint/tests:

```bash
go test ./...
```

Ensure `gofmt` passes:

```bash
gofmt -w cmd/finder/main.go
```
