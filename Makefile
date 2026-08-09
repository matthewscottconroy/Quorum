.PHONY: build run dev test test-web test-integration lint arcade arcade-test backup backup-list restore backup-verify backups-install \
        local local-down local-reset local-restart local-logs local-status \
        pod-up pod-down pod-build pod-push \
        docker-up docker-down \
        secret bootstrap help

# ── Build ─────────────────────────────────────────────────────────────────────

build:
	go build -o ./quorum ./cmd/quorum

# Optional: build the Top Secret arcade cartridge (Rust → wasm) into
# web/arcade/, which is embedded into the binary on the NEXT `make build`.
# Requires: rustup target add wasm32-unknown-unknown
#           cargo install wasm-bindgen-cli --version 0.2.126
# Without this the app runs fine; the arcade page reports the cartridge
# as not installed.
arcade:
	./arcade/build.sh

arcade-test:
	cd arcade && cargo test -p arcade-logic

run: build
	./quorum

dev:
	QUORUM_DATABASE_URL=$$(grep QUORUM_DATABASE_URL .env | cut -d= -f2-) \
	QUORUM_JWT_SECRET=$$(grep QUORUM_JWT_SECRET .env | cut -d= -f2-) \
	go run ./cmd/quorum

test:
	go test -race -count=1 ./...

test-web:
	node --test web/*.test.js

# Integration tests against a real Postgres. Set QUORUM_TEST_DATABASE_URL, e.g.
#   QUORUM_TEST_DATABASE_URL=postgres://quorum:test@localhost:55432/quorum?sslmode=disable make test-integration
test-integration:
	go test -tags integration -count=1 ./internal/...

lint:
	golangci-lint run

# Scan for known vulnerabilities reachable from our code (also runs in CI,
# weekly and on every push — findings appear over time, not with commits).
vulncheck:
	govulncheck ./...

# ── Backups / disaster recovery ───────────────────────────────────────────────

backup:
	scripts/backup.sh create

backup-list:
	scripts/backup.sh list

# Restore a dump into the live DB (destructive): make restore FILE=backups/quorum-....pgdump
restore:
	scripts/backup.sh restore "$(FILE)"

# Prove the latest (or FILE=...) backup is restorable, into a throwaway DB.
backup-verify:
	scripts/backup.sh verify "$(FILE)"

# Install nightly-backup + weekly-verify + daily audit-chain systemd timers
# pointed at this checkout (root: system-wide; otherwise per-user).
backups-install:
	ops/install-backup-timers.sh

# Render USER_MANUAL.md to quorum-manual.pdf (pandoc + LaTeX, or Chrome fallback).
manual-pdf:
	scripts/manual-pdf.sh

# Point git at the tracked hooks (pre-commit: gofmt+vet, pre-push: lint+tests).
hooks-install:
	chmod +x scripts/git-hooks/*
	git config core.hooksPath scripts/git-hooks
	@echo "hooks installed: pre-commit (gofmt, vet), pre-push (lint, tests)"

# ── Local stack via plain Podman (no compose provider needed) ─────────────────
# Runs Postgres + the app in one Podman pod (shared netns → app reaches the DB
# on localhost:5432, matching .env). Works with just `podman` — no
# podman-compose / docker-compose required. See scripts/local.sh.

local:
	scripts/local.sh up

local-down:
	scripts/local.sh down

local-reset:
	scripts/local.sh reset

local-restart:
	scripts/local.sh restart

local-logs:
	scripts/local.sh logs

local-status:
	scripts/local.sh status

# ── Podman Compose (local development) ────────────────────────────────────────
# Requires Podman >= 4.7 with a compose provider (podman-compose or
# docker-compose). If you don't have one, use `make local` above instead.
# The Containerfile/Dockerfile format is identical; no changes needed.

# IMAGE can be overridden: make pod-build IMAGE=my-registry.io/quorum:dev
IMAGE ?= localhost/quorum:dev

pod-up:
	podman compose up --build -d

pod-down:
	podman compose down

pod-build:
	podman build --format oci -t $(IMAGE) .

pod-push:
	podman push $(IMAGE)

pod-run: pod-build
	podman run --rm -p 127.0.0.1:8080:8080 \
	  --env-file .env \
	  $(IMAGE)

# ── Docker aliases (backwards-compatible) ────────────────────────────────────

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down

# ── Helpers ───────────────────────────────────────────────────────────────────

# Generate a random JWT secret and print it.
secret:
	@openssl rand -hex 32

# Bootstrap the first admin user (run once after first start).
bootstrap:
	@read -p "Email: " email; \
	read -sp "Password: " pw; echo; \
	curl -s -X POST http://localhost:8080/api/v1/auth/bootstrap \
	  -H 'Content-Type: application/json' \
	  -d "{\"email\":\"$$email\",\"password\":\"$$pw\"}" | python3 -m json.tool

help:
	@echo "Quorum Makefile targets:"
	@echo ""
	@echo "  build           Build the Go binary"
	@echo "  run             Build and run the binary"
	@echo "  dev             Run with .env loaded (no Docker)"
	@echo "  test            Run go test -race ./..."
	@echo "  test-web        Run frontend unit tests (node --test)"
	@echo "  test-integration Run integration tests (needs QUORUM_TEST_DATABASE_URL)"
	@echo "  backup          Dump the database to backups/ (prunes old ones)"
	@echo "  backup-list     List existing backups"
	@echo "  backup-verify   Prove the latest backup restores into a scratch DB"
	@echo "  restore         Restore a dump into the live DB (FILE=... , destructive)"
	@echo "  backups-install Install nightly backup/verify systemd timers for this checkout"
	@echo "  lint            Run golangci-lint"
	@echo "  vulncheck       Scan for reachable known vulnerabilities"
	@echo "  manual-pdf      Export USER_MANUAL.md as quorum-manual.pdf"
	@echo ""
	@echo "  local           Start the local stack with plain Podman (no compose needed)"
	@echo "  local-down      Stop the local stack (keeps the database volume)"
	@echo "  local-reset     Stop and wipe the local database volume"
	@echo "  local-restart   Rebuild the image and restart the app container"
	@echo "  local-logs      Follow the app logs"
	@echo "  local-status    Show pod/container status"
	@echo ""
	@echo "  pod-up          Start with Podman Compose (builds image)"
	@echo "  pod-down        Stop Podman Compose stack"
	@echo "  pod-build       Build OCI image with Podman  (IMAGE=...)"
	@echo "  pod-push        Push image to registry      (IMAGE=...)"
	@echo "  pod-run         Build and run container locally"
	@echo ""
	@echo "  docker-up       Start with Docker Compose (alias)"
	@echo "  docker-down     Stop Docker Compose stack  (alias)"
	@echo ""
	@echo "  secret          Generate a random JWT secret"
	@echo "  bootstrap       Create the first admin user (interactive)"
