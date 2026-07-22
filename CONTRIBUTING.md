# Contributing to Quorum

Thanks for your interest in improving Quorum. This guide covers the practical
essentials: building, testing, code style, migrations, and the sign-off we
require on every commit.

## Prerequisites

- **Go 1.23+**
- **PostgreSQL 16** (only needed to run the app; the test suite needs no database)
- **golangci-lint** for linting: `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`
- **Podman ≥ 4.7** or Docker, if you want to run the full container stack

## Build and test

The `Makefile` wraps the common commands:

```sh
make build          # go build -o ./quorum ./cmd/quorum
make test           # go test -race -count=1 ./...
make lint           # golangci-lint run
make dev            # go run ./cmd/quorum (reads QUORUM_* from .env)
make secret         # print a random 32-byte hex JWT secret
```

Run the tests directly when you want to scope them:

```sh
go test ./...                          # whole module
go test -race ./...                    # with the race detector (what CI runs)
go test -v ./internal/handler/...      # one package, verbose
```

Handler tests use lightweight function-field mocks (see
`internal/handler/testhelpers_test.go`) that satisfy the package-private
interfaces in `internal/handler/interfaces.go`, so **no database is required**
to run the suite. New handlers should follow the existing pattern: add a mock
for the repo, then cover the success path, validation errors, repo errors, and
any query-parameter passthrough.

## Code style

- **`gofmt`** — all code must be gofmt-clean. Run `gofmt -l .` (lists
  unformatted files) or `go fmt ./...`. Most editors format on save.
- **`go vet ./...`** — must pass with no findings.
- **`golangci-lint run`** (via `make lint`) — must pass before you open a PR.
- Match the surrounding code. Nullable SQL columns map to pointer types
  (`*string`, `*time.Time`, …) in model structs; handlers stay thin and delegate
  to the service/repo layers.

## Database migrations

Migrations are numbered SQL files in `internal/db/migrations/`, applied
automatically at startup by an embedded runner that takes a PostgreSQL advisory
lock (so parallel instance starts don't double-apply). There is no external
migration tool to run.

To add a migration, create a matching **up/down** pair with the next sequence
number:

```
internal/db/migrations/0007_add_my_table.up.sql
internal/db/migrations/0007_add_my_table.down.sql
```

The runner applies files in numeric order and skips already-applied versions
(tracked in the `schema_migrations` table). Restart the app to apply new
migrations. Keep the `.down.sql` a faithful inverse of the `.up.sql`, and make
statements idempotent (`IF NOT EXISTS` / `IF EXISTS`) where practical. The
migrations are the authoritative schema — keep the `DESIGN.md` data-model notes
in sync when you change them.

## Commit sign-off (DCO)

Quorum accepts contributions under the **Developer Certificate of Origin**
([DCO](https://developercertificate.org/)). Every commit must carry a
`Signed-off-by` line certifying that you wrote the change (or otherwise have the
right to submit it) and may contribute it under the project's license:

```sh
git commit -s -m "your message"
```

The `-s` flag appends:

```
Signed-off-by: Your Name <you@example.com>
```

using your configured `user.name` and `user.email`. PRs whose commits are not
signed off will be asked to amend (`git commit -s --amend`, or
`git rebase --signoff` for a series).

## Pull requests

Before opening a PR:

1. `make test` passes (race detector clean).
2. `make lint`, `go vet ./...`, and `gofmt -l .` are clean.
3. Commits are signed off (`-s`).
4. Docs (`README.md`, `DESIGN.md`, `SECURITY.md`, `DEPLOYMENT.md`) are updated if
   your change affects behavior, configuration, the schema, or the API surface.

## License

Quorum is licensed under the **GNU Affero General Public License v3.0 or later**
(AGPL-3.0-or-later). By submitting a contribution and signing off under the DCO,
you agree that your contribution is provided under this same license. See
[LICENSE](LICENSE) and [NOTICE](NOTICE).
