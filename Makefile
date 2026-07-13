.PHONY: build run dev test lint \
        pod-up pod-down pod-build pod-push \
        docker-up docker-down \
        secret bootstrap help

# ── Build ─────────────────────────────────────────────────────────────────────

build:
	go build -o ./quorum ./cmd/quorum

run: build
	./quorum

dev:
	QUORUM_DATABASE_URL=$$(grep QUORUM_DATABASE_URL .env | cut -d= -f2-) \
	QUORUM_JWT_SECRET=$$(grep QUORUM_JWT_SECRET .env | cut -d= -f2-) \
	go run ./cmd/quorum

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run

# ── Podman (local development) ────────────────────────────────────────────────
# Requires Podman >= 4.7 (podman compose built-in) or the podman-compose package.
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
	@echo "  lint            Run golangci-lint"
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
