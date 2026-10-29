# Source provenance

The Nostr protocol, relay engine, and PostgreSQL storage implementation are maintained in this repository. The versions below are their upstream baselines, not Go module dependencies. General-purpose libraries, including cryptography, WebSocket, SQL, and OpenTelemetry dependencies, remain managed through `go.mod`.

## Upstream baselines

| Upstream module | Version | Commit | Local package |
| --- | --- | --- | --- |
| `github.com/nbd-wtf/go-nostr` | `v0.52.3` | `78da15566351c20b57eff50907844607a06d9a10` | [`internal/nostr/`](../internal/nostr/) |
| `github.com/fiatjaf/khatru` | `v0.19.1` | `8d5fc324f7a0bb2af083d8566b531ca3357ee652` | [`internal/relay/engine/`](../internal/relay/engine/) |
| `github.com/fiatjaf/eventstore` | `v0.17.5` | `4983a415892bea9ff4d2f60e1900651ebedb98ee` | [`internal/storage/`](../internal/storage/) |

Go module content checksums for these baselines:

```text
github.com/nbd-wtf/go-nostr v0.52.3 h1:Xd87pXfJEJRXHpM+fLjQQln8dBNNaoPA10V7BbyP4KI=
github.com/fiatjaf/khatru v0.19.1 h1:n2m+cL9pdeb8WMhIDYbjct7jCirS9eHuMR0R7i2JGjw=
github.com/fiatjaf/eventstore v0.17.5 h1:/3CRthtZmkcTM01IxEiebydM5MKYZhZcQBKMSbLuWCs=
```

## Source scope

- **go-nostr:** the root protocol and client package; `nip11`, `nip40`, `nip42`, `nip45`, `nip70`, `nip77`, and `nip86`; and their HyperLogLog and negentropy support packages.
- **khatru:** the root relay implementation and `policies` package. `internal/relay/` connects the engine to application-specific policies and PostgreSQL.
- **eventstore:** storage interfaces, shared errors and helpers, the PostgreSQL backend, and its internal support package. Other database backends are outside this implementation.

See [Directory layout](directory.md) for package responsibilities and dependency boundaries.

## Local adaptations

- Imports and package names follow the local layout.
- Error identifiers use `Err`-prefixed names, such as `nostr.ErrUnknownLabel` and `postgres.ErrTooManyIDs`. Relay context keys use a package-specific type.
- Signature handling and JSON envelope parsing use Go implementations. The upstream cgo, Sonic, JavaScript, and debug-logging build variants are not maintained. Release builds target Linux amd64 and arm64 with `CGO_ENABLED=0`.

Protocol encoding, event ID and signature semantics, and the PostgreSQL storage format follow the baselines above. Changes to event or filter serialization must also account for the checked-in generated JSON code.

## PostgreSQL compatibility

The PostgreSQL schema, indexes, `tags_to_tagvalues` function, and tag encoding are compatible with eventstore v0.17.5. Existing databases using that format can be reused.

The later upstream [tag-encoding change](https://github.com/fiatjaf/eventstore/commit/359b43d6010b4060808de4720524db5756370b91) is not part of this baseline. Adopting it requires an explicit migration plan for existing events and tag indexes; updating the Go implementation alone is not sufficient.

## Maintaining these packages

Modify the local packages directly. Updating dependencies with `go get` does not update these implementations.

When porting an upstream fix, compare it against the recorded baseline and account for the local adaptations and package boundaries. When adopting a new baseline, update its version, commit, and module checksum here, and review its license notices.

Run the repository checks and both release builds:

```sh
make check
make bin GOARCH=amd64 BUILD_DIR=bin/amd64
make bin GOARCH=arm64 BUILD_DIR=bin/arm64
```

For protocol or persistence changes, also exercise the affected paths against PostgreSQL: existing event and tag reads, publication and subscriptions, duplicate handling, replacement and deletion, authentication, and shutdown on database errors. Storage-format changes require migration verification in addition to these checks.

## Licensing and distribution

- **go-nostr:** MIT, Copyright (c) 2022 nbd. Preserve [`internal/nostr/LICENSE.md`](../internal/nostr/LICENSE.md) and its copyright notice.
- **khatru and eventstore:** Unlicense. Preserve the license files in [`internal/relay/engine/`](../internal/relay/engine/LICENSE) and [`internal/storage/`](../internal/storage/LICENSE).

Distribute the project [`LICENSE`](../LICENSE) and [`THIRD_PARTY_NOTICES`](../THIRD_PARTY_NOTICES) with source and binary releases. `make bin` places both files in the output directory's `licenses/` subdirectory; the Docker image contains them at `/usr/share/licenses/khatru-redbean/`.

External dependencies retain their own license requirements and must be reviewed separately when updating `go.mod`.
