# urbex-cli

The `urbex` CLI — implementation repo. For architecture, ADRs, and the
manifest/platform spec, see [urbforge/urbex](https://github.com/urbforge/urbex).

## Status

Scaffold stage. Implemented so far:

- `urbex init` — scaffolds a starter `urbex.yaml` app manifest, or
  validates an existing one against the manifest schema.

Every other subcommand (`bootstrap`, `plan`, `apply`, `deploy`,
`promote`, `status`, `destroy`) exists as a stub that points to the
relevant ADR in `urbforge/urbex` and fails with "not implemented yet".

## Build & test

```sh
go build -o urbex ./cmd/urbex
go test ./...
```

## Try it

```sh
go run ./cmd/urbex init          # scaffolds urbex.yaml in the current directory
# ... edit urbex.yaml to declare a frontend and/or services ...
go run ./cmd/urbex init          # validates it
```

## Layout

```
cmd/urbex/            main package, CLI entry point
internal/cli/          cobra command tree (one file per command)
internal/manifest/      urbex.yaml loading, validation, scaffolding
internal/manifest/schema/  vendored copy of the manifest JSON Schema
                            (source of truth: urbforge/urbex/schemas/urbex.schema.json)
```

## Note on the vendored schema

`internal/manifest/schema/urbex.schema.json` is a copy of the schema
maintained in [urbforge/urbex](https://github.com/urbforge/urbex). It
must be kept in sync manually for now; see
[ADR-0016](https://github.com/urbforge/urbex/blob/main/docs/decisions/0016-cli-repo-split.md)
in the docs repo for the reasoning and the plan to automate this.
