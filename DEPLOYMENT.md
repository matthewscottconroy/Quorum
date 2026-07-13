# Quorum Deployment Guide

This guide covers every supported deployment path from a laptop to a production Kubernetes cluster.

---

## Table of Contents

1. [Environment variables reference](#1-environment-variables-reference)
2. [Local development](#2-local-development)
   - 2.1 [Prerequisites](#21-prerequisites)
   - 2.2 [First-time setup](#22-first-time-setup)
   - 2.3 [Mode A — Go binary only (fastest iteration)](#23-mode-a--go-binary-only-fastest-iteration)
   - 2.4 [Mode B — Podman Compose (recommended)](#24-mode-b--podman-compose-recommended)
   - 2.5 [Mode C — Docker Compose (alternative)](#25-mode-c--docker-compose-alternative)
   - 2.6 [Bootstrap the first admin user](#26-bootstrap-the-first-admin-user)
   - 2.7 [Day-to-day development workflow](#27-day-to-day-development-workflow)
3. [Staging (non-production Kubernetes)](#3-staging-non-production-kubernetes)
   - 3.1 [What staging differs from production](#31-what-staging-differs-from-production)
   - 3.2 [Deploy with Kustomize](#32-deploy-with-kustomize)
   - 3.3 [Deploy with Helm](#33-deploy-with-helm)
   - 3.4 [Staging secrets](#34-staging-secrets)
   - 3.5 [Verifying a staging deploy](#35-verifying-a-staging-deploy)
4. [Production (Kubernetes — GitOps)](#4-production-kubernetes--gitops)
   - 4.1 [Architecture overview](#41-architecture-overview)
   - 4.2 [Cluster prerequisites](#42-cluster-prerequisites)
   - 4.3 [One-time cluster setup](#43-one-time-cluster-setup)
   - 4.4 [Production secrets](#44-production-secrets)
   - 4.5 [Tekton CI pipeline](#45-tekton-ci-pipeline)
   - 4.6 [Argo CD GitOps setup](#46-argo-cd-gitops-setup)
   - 4.7 [Deploy with Helm (non-GitOps alternative)](#47-deploy-with-helm-non-gitops-alternative)
   - 4.8 [Rolling updates and rollbacks](#48-rolling-updates-and-rollbacks)
   - 4.9 [Database management](#49-database-database-management)
   - 4.10 [TLS and ingress](#410-tls-and-ingress)
   - 4.11 [Network policy](#411-network-policy)
   - 4.12 [Observability](#412-observability)
5. [Secrets management deep-dive](#5-secrets-management-deep-dive)
6. [Container image management](#6-container-image-management)
7. [Troubleshooting](#7-troubleshooting)

---

## 1. Environment variables reference

All runtime configuration is passed through environment variables. No configuration files are read at runtime.

| Variable | Required | Default | Description |
|---|---|---|---|
| `QUORUM_DATABASE_URL` | **yes** | — | PostgreSQL DSN. Example: `postgres://quorum:pass@host:5432/quorum?sslmode=require` |
| `QUORUM_JWT_SECRET` | **yes** | — | HS256 signing key. Minimum 32 bytes. Generate: `openssl rand -hex 32` |
| `QUORUM_PORT` | no | `8080` | HTTP listen port |
| `QUORUM_BASE_URL` | no | `http://localhost:8080` | Public URL used in email links |
| `QUORUM_JWT_ACCESS_TTL` | no | `15m` | Access token lifetime (Go duration string, e.g. `15m`, `1h`) |
| `QUORUM_JWT_REFRESH_TTL` | no | `168h` | Refresh token lifetime (default 7 days) |
| `QUORUM_SMTP_HOST` | no | — | SMTP hostname. Leave empty to disable email reminders entirely |
| `QUORUM_SMTP_PORT` | no | `587` | SMTP port. Only read when `QUORUM_SMTP_HOST` is set |
| `QUORUM_SMTP_USER` | no | — | SMTP authentication username |
| `QUORUM_SMTP_PASS` | no | — | SMTP authentication password (secret) |
| `QUORUM_EMAIL_FROM` | no | `quorum@localhost` | From address for outbound email |
| `QUORUM_STRIPE_WEBHOOK_SECRET` | no | — | Stripe webhook signing secret (`whsec_…`). Omit to skip signature verification (dev only) |
| `QUORUM_PAYPAL_WEBHOOK_ID` | no | — | PayPal webhook ID for signature verification |
| `DB_PASSWORD` | Compose only | — | Postgres container password (used only in `compose.yaml`) |

Variables split by sensitivity:

- **ConfigMap** (non-secret, commit-safe): `QUORUM_PORT`, `QUORUM_BASE_URL`, `QUORUM_JWT_ACCESS_TTL`, `QUORUM_JWT_REFRESH_TTL`, `QUORUM_SMTP_HOST`, `QUORUM_SMTP_PORT`, `QUORUM_SMTP_USER`, `QUORUM_EMAIL_FROM`
- **Secret** (never commit): `QUORUM_DATABASE_URL`, `QUORUM_JWT_SECRET`, `QUORUM_SMTP_PASS`, `QUORUM_STRIPE_WEBHOOK_SECRET`, `QUORUM_PAYPAL_WEBHOOK_ID`

---

## 2. Local development

### 2.1 Prerequisites

| Tool | Minimum version | Install |
|---|---|---|
| Go | 1.23 | https://go.dev/dl/ |
| Podman | 4.7 | `dnf install podman` / `brew install podman` |
| podman-compose | any | included in Podman ≥ 4.7 or `pip install podman-compose` |
| PostgreSQL client | 16 | optional, for direct `psql` access |
| golangci-lint | 1.57 | `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest` |

Docker can be used instead of Podman for all Compose operations — see [Mode C](#25-mode-c--docker-compose-alternative).

### 2.2 First-time setup

```sh
# Clone the repository
git clone git@github.com:your-org/quorum.git
cd quorum

# Copy environment template
cp .env.example .env

# Generate a JWT secret and paste it into .env as QUORUM_JWT_SECRET
make secret
# → prints something like: a3f2e1c4d5b6...  (64 hex chars)

# Edit .env — at minimum set:
#   QUORUM_JWT_SECRET=<output from above>
#   DB_PASSWORD=<any local password, e.g. devpassword>
#   QUORUM_DATABASE_URL=postgres://quorum:devpassword@localhost:5432/quorum?sslmode=disable
```

`.env` is listed in `.gitignore` and must never be committed.

### 2.3 Mode A — Go binary only (fastest iteration)

This mode requires a PostgreSQL instance running separately (local install or a container started manually).

```sh
# Start a throwaway Postgres container (one-time or per session)
podman run -d --name quorum-db \
  -e POSTGRES_USER=quorum \
  -e POSTGRES_PASSWORD=devpassword \
  -e POSTGRES_DB=quorum \
  -p 5432:5432 \
  postgres:16-alpine

# Run the app — reads QUORUM_DATABASE_URL and QUORUM_JWT_SECRET from .env
make dev
```

`make dev` uses shell substitution to read only the two required variables from `.env` — you do not need to `source .env` or use `direnv`. All other variables fall back to their defaults.

The app starts at `http://localhost:8080`. The migration runner executes automatically on startup — no manual `migrate` step.

Restart by pressing `Ctrl-C` and re-running `make dev`. There is no hot-reload; the rebuild is fast (<2 seconds for incremental builds).

### 2.4 Mode B — Podman Compose (recommended)

Podman Compose starts both the PostgreSQL container and the app container, wiring them together on an isolated network. The image is rebuilt automatically on `pod-up` if source files have changed.

```sh
# Start the full stack (builds image, starts postgres + app)
make pod-up

# View logs
podman compose logs -f

# Stop and remove containers (data volume is preserved)
make pod-down

# Stop and destroy data volume (full reset)
podman compose down -v
```

The app is available at `http://localhost:8080`. PostgreSQL data is stored in the `pgdata` named Podman volume and survives `pod-down`.

**Rebuilding after code changes:**

```sh
# Rebuild and restart the app container only (Postgres keeps running)
podman compose up --build -d quorum
```

**Overriding the image name:**

```sh
IMAGE=registry.example.com/quorum:dev make pod-build
IMAGE=registry.example.com/quorum:dev make pod-push
```

### 2.5 Mode C — Docker Compose (alternative)

For teams that use Docker instead of Podman:

```sh
make docker-up     # docker compose up --build -d
make docker-down   # docker compose down
```

All `docker compose` and `podman compose` commands are interchangeable — the `compose.yaml` file is identical.

### 2.6 Bootstrap the first admin user

Run this once after the first start, regardless of which mode you chose:

```sh
make bootstrap
# → prompts for Email and Password
# → calls POST /api/v1/auth/bootstrap
# → prints the created user as JSON
```

The bootstrap endpoint returns `403 Forbidden` if any user already exists. It is safe to call multiple times — subsequent calls are no-ops that return 403.

To bootstrap manually (useful in scripts):

```sh
curl -s -X POST http://localhost:8080/api/v1/auth/bootstrap \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"changeme"}' \
  | python3 -m json.tool
```

### 2.7 Day-to-day development workflow

```sh
# Run all tests with race detector
make test
# equivalent to: go test -race -count=1 ./...

# Run only handler tests with verbose output
go test -v ./internal/handler/...

# Lint
make lint
# equivalent to: golangci-lint run

# Build the static binary
make build
# output: ./quorum

# Scan for known CVEs
govulncheck ./...

# Apply a new database migration
# 1. Create the file:
#    internal/db/migrations/0002_my_change.up.sql
# 2. Restart the app — the migration runner applies it automatically.
```

**Handler tests** use lightweight in-memory mocks and require no database. They run in under a second:

```sh
go test -v -run TestBootstrap ./internal/handler/...
```

**Frontend development** — the `web/` directory is embedded into the binary at build time via `//go:embed`. During development with `make dev`, changes to `web/` require a restart. There is no build step, no npm, and no bundler.

---

## 3. Staging (non-production Kubernetes)

Staging mirrors production configuration at smaller resource footprints. It is intended for integration testing, QA sign-off, and pre-release validation.

### 3.1 What staging differs from production

| Aspect | Staging | Production |
|---|---|---|
| Kubernetes namespace | `quorum-staging` | `quorum` |
| Replicas | 1 | 2 |
| CPU request / limit | 50m / 200m | 200m / 1000m |
| Memory request / limit | 32Mi / 128Mi | 128Mi / 512Mi |
| Domain | `quorum-staging.example.com` | `quorum.example.com` |
| Image tag | `latest` (or pinned manually) | commit SHA (set by Tekton) |
| Secret management | Sealed Secret or manual | Sealed Secrets / ESO |
| Argo CD auto-sync | optional | enabled |

### 3.2 Deploy with Kustomize

The staging overlay is at `deploy/kustomize/overlays/staging/`. It applies two patches on top of `deploy/kustomize/base/`:

- `patches/configmap.yaml` — sets `QUORUM_BASE_URL` to the staging domain
- `patches/resources.yaml` — sets 1 replica and minimal CPU/memory limits

Before applying, update the image tag and any domain references:

```sh
# Preview what will be applied (dry run)
kubectl kustomize deploy/kustomize/overlays/staging

# Apply the staging overlay
kubectl apply -k deploy/kustomize/overlays/staging

# Or pin a specific image tag first:
cd deploy/kustomize/overlays/staging
kustomize edit set image registry.example.com/quorum=registry.example.com/quorum:abc1234
git add kustomization.yaml && git commit -m "chore: pin staging to abc1234"
kubectl apply -k .
```

To deploy a specific commit SHA to staging without modifying git:

```sh
# Override image tag at apply time (does not persist to git)
kubectl set image deployment/quorum \
  quorum=registry.example.com/quorum:abc1234 \
  -n quorum-staging
```

### 3.3 Deploy with Helm

```sh
# First install (creates namespace automatically)
helm upgrade --install quorum-staging deploy/helm/quorum \
  --namespace quorum-staging \
  --create-namespace \
  -f deploy/helm/quorum/values.yaml \
  --set replicaCount=1 \
  --set config.baseUrl=https://quorum-staging.example.com \
  --set ingress.hosts[0].host=quorum-staging.example.com \
  --set ingress.tls[0].hosts[0]=quorum-staging.example.com \
  --set ingress.tls[0].secretName=quorum-staging-tls \
  --set image.tag=abc1234

# Upgrade to a new image tag
helm upgrade quorum-staging deploy/helm/quorum \
  --namespace quorum-staging \
  --reuse-values \
  --set image.tag=def5678

# View current values
helm get values quorum-staging -n quorum-staging

# Uninstall
helm uninstall quorum-staging -n quorum-staging
```

### 3.4 Staging secrets

The `quorum-secrets` Kubernetes Secret must exist in `quorum-staging` before the first deploy.

**Option 1 — Manual (acceptable for staging):**

```sh
kubectl create secret generic quorum-secrets \
  --namespace quorum-staging \
  --from-literal=QUORUM_DATABASE_URL="postgres://quorum:pass@postgres.quorum-staging.svc:5432/quorum?sslmode=require" \
  --from-literal=QUORUM_JWT_SECRET="$(openssl rand -hex 32)" \
  --from-literal=QUORUM_SMTP_PASS="" \
  --from-literal=QUORUM_STRIPE_WEBHOOK_SECRET="" \
  --from-literal=QUORUM_PAYPAL_WEBHOOK_ID=""
```

**Option 2 — Sealed Secrets (recommended even for staging):**

```sh
# Encrypt locally
kubectl create secret generic quorum-secrets \
  --namespace quorum-staging \
  --from-literal=QUORUM_DATABASE_URL="..." \
  --from-literal=QUORUM_JWT_SECRET="..." \
  --dry-run=client -o yaml \
  | kubeseal --controller-namespace sealed-secrets \
  > deploy/kustomize/overlays/staging/sealed-secret.yaml

# Commit sealed-secret.yaml — it is safe to store in git
git add deploy/kustomize/overlays/staging/sealed-secret.yaml
```

Add `sealed-secret.yaml` to the staging `kustomization.yaml` resources list so it is applied alongside the overlay.

### 3.5 Verifying a staging deploy

```sh
# Check pod status
kubectl get pods -n quorum-staging

# Check rollout
kubectl rollout status deployment/quorum -n quorum-staging

# Tail logs
kubectl logs -n quorum-staging -l app.kubernetes.io/name=quorum -f

# Quick health check
curl -s https://quorum-staging.example.com/api/v1/auth/bootstrap | python3 -m json.tool
# Expect: {"error":"forbidden"} or {"message":"..."} — not a 5xx

# Run bootstrap on staging (first time only)
curl -s -X POST https://quorum-staging.example.com/api/v1/auth/bootstrap \
  -H 'Content-Type: application/json' \
  -d '{"email":"staging-admin@example.com","password":"..."}' \
  | python3 -m json.tool
```

---

## 4. Production (Kubernetes — GitOps)

### 4.1 Architecture overview

```
Developer pushes to main branch
  │
  ▼
GitHub/Gitea webhook (HMAC-validated)
  │
  ▼
Tekton EventListener (quorum-listener)
  │
  ▼
Tekton Pipeline: quorum-build
  ├── Task 1: git-clone         Clone source at commit SHA
  ├── Task 2: quorum-go-test    go test -race ./...
  ├── Task 3: quorum-buildah-build
  │             buildah bud → push <registry>/quorum:<sha>
  │             buildah bud → push <registry>/quorum:latest
  └── Task 4: quorum-update-manifest
                Clone GitOps repo
                kustomize edit set image <registry>/quorum=<registry>/quorum:<sha>
                git commit -m "chore: deploy quorum <sha>"
                git push → GitOps repo
                  │
                  ▼
              Argo CD detects GitOps repo change
                  │
                  ▼
              kubectl apply -k deploy/kustomize/overlays/production
                  │
                  ▼
              Kubernetes rolling update (0 downtime)
```

The application never knows it is being deployed — Argo CD applies the updated Kustomize overlay and Kubernetes performs a rolling update. The old pod continues serving traffic until the new pod passes readiness checks.

### 4.2 Cluster prerequisites

Install these in your cluster before deploying Quorum:

| Component | Purpose | Install |
|---|---|---|
| Tekton Pipelines | CI task runner | `kubectl apply -f https://storage.googleapis.com/tekton-releases/pipeline/latest/release.yaml` |
| Tekton Triggers | Webhook → PipelineRun | `kubectl apply -f https://storage.googleapis.com/tekton-releases/triggers/latest/release.yaml` |
| Argo CD | GitOps controller | https://argo-cd.readthedocs.io/en/stable/getting_started/ |
| ingress-nginx | Ingress controller | `helm install ingress-nginx ingress-nginx/ingress-nginx` |
| cert-manager | Automatic TLS (Let's Encrypt) | `helm install cert-manager jetstack/cert-manager --set installCRDs=true` |
| PostgreSQL | Database | Bitnami chart or managed (Cloud SQL, RDS, etc.) |
| Sealed Secrets (optional) | Encrypted secrets in git | `helm install sealed-secrets sealed-secrets/sealed-secrets` |

Minimum node resources for the Quorum workload itself: 200m CPU / 128Mi memory. Tekton task pods require additional capacity while builds run.

### 4.3 One-time cluster setup

Run these commands once when setting up a new cluster. They are idempotent.

```sh
# 1. Apply Tekton RBAC (service account for triggers)
kubectl apply -f deploy/tekton/rbac.yaml

# 2. Apply custom Tekton tasks
kubectl apply -f deploy/tekton/tasks.yaml

# 3. Apply the CI pipeline definition
kubectl apply -f deploy/tekton/pipeline.yaml

# 4. Apply the EventListener and trigger bindings
kubectl apply -f deploy/tekton/triggers/event-listener.yaml

# 5. Register the Argo CD AppProject
kubectl apply -f deploy/argocd/project.yaml

# 6. Register the Argo CD Application (Kustomize overlay)
kubectl apply -f deploy/argocd/application.yaml
```

After step 4, retrieve the EventListener service address and point your webhook at it:

```sh
kubectl get svc -n tekton-pipelines | grep quorum-listener
# el-quorum-listener   ClusterIP   10.96.x.x   <none>   8080/TCP

# If using an external webhook: expose via Ingress or LoadBalancer,
# then configure the GitHub/Gitea webhook to POST to:
#   http://<external-ip>:8080/
# with Content-Type: application/json and Secret: <github-webhook-secret token>
```

### 4.4 Production secrets

Production secrets are never stored in plain text in git. Two approaches are supported:

#### Option A — Sealed Secrets (recommended for self-hosted)

Sealed Secrets encrypts a Kubernetes Secret using the cluster's public key. The encrypted `SealedSecret` resource is safe to commit to git. The controller decrypts it in-cluster.

```sh
# Install kubeseal CLI
# https://github.com/bitnami-labs/sealed-secrets#kubeseal

# Generate and encrypt the quorum-secrets secret
kubectl create secret generic quorum-secrets \
  --namespace quorum \
  --from-literal=QUORUM_DATABASE_URL="postgres://quorum:<pass>@postgres.quorum.svc:5432/quorum?sslmode=require" \
  --from-literal=QUORUM_JWT_SECRET="$(openssl rand -hex 32)" \
  --from-literal=QUORUM_SMTP_PASS="<smtp-password>" \
  --from-literal=QUORUM_STRIPE_WEBHOOK_SECRET="whsec_..." \
  --from-literal=QUORUM_PAYPAL_WEBHOOK_ID="<webhook-id>" \
  --dry-run=client -o yaml \
  | kubeseal \
      --controller-namespace sealed-secrets \
      --controller-name sealed-secrets \
  > deploy/kustomize/overlays/production/sealed-secret.yaml

# Commit the sealed secret
git add deploy/kustomize/overlays/production/sealed-secret.yaml
git commit -m "chore: add production sealed secret"
git push
```

Add `sealed-secret.yaml` to `deploy/kustomize/overlays/production/kustomization.yaml` under `resources:`.

#### Option B — External Secrets Operator (recommended for cloud)

ESO syncs secrets from an external store (Vault, AWS Secrets Manager, GCP Secret Manager, Azure Key Vault) into Kubernetes Secrets automatically.

```yaml
# deploy/kustomize/overlays/production/external-secret.yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: quorum-secrets
  namespace: quorum
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend   # configure to match your SecretStore
    kind: SecretStore
  target:
    name: quorum-secrets
    creationPolicy: Owner
  data:
    - secretKey: QUORUM_DATABASE_URL
      remoteRef:
        key: secret/quorum/production
        property: database_url
    - secretKey: QUORUM_JWT_SECRET
      remoteRef:
        key: secret/quorum/production
        property: jwt_secret
    - secretKey: QUORUM_SMTP_PASS
      remoteRef:
        key: secret/quorum/production
        property: smtp_pass
    - secretKey: QUORUM_STRIPE_WEBHOOK_SECRET
      remoteRef:
        key: secret/quorum/production
        property: stripe_webhook_secret
    - secretKey: QUORUM_PAYPAL_WEBHOOK_ID
      remoteRef:
        key: secret/quorum/production
        property: paypal_webhook_id
```

#### Tekton-specific secrets

The Tekton pipeline also requires two secrets in `tekton-pipelines`:

```sh
# Registry credentials (Podman/Docker auth format)
# 1. Log in to your registry with Podman:
podman login registry.example.com

# 2. Create the secret from the Podman auth file:
kubectl create secret generic registry-credentials \
  --namespace tekton-pipelines \
  --from-file=.dockerconfigjson=$HOME/.config/containers/auth.json \
  --type=kubernetes.io/dockerconfigjson

# GitOps repo SSH deploy key (write access to the GitOps repository)
ssh-keygen -t ed25519 -C "tekton@quorum-ci" -f /tmp/quorum-gitops-deploy
# Add /tmp/quorum-gitops-deploy.pub as a Deploy Key (with write access)
# in GitHub/Gitea settings for the GitOps repository.

kubectl create secret generic gitops-ssh-key \
  --namespace tekton-pipelines \
  --from-file=id_ed25519=/tmp/quorum-gitops-deploy
rm /tmp/quorum-gitops-deploy /tmp/quorum-gitops-deploy.pub

# HMAC webhook secret (shared with GitHub/Gitea)
WEBHOOK_TOKEN=$(openssl rand -hex 32)
echo "Set this token in your GitHub/Gitea webhook settings: $WEBHOOK_TOKEN"
kubectl create secret generic github-webhook-secret \
  --namespace tekton-pipelines \
  --from-literal=token="$WEBHOOK_TOKEN"
```

### 4.5 Tekton CI pipeline

#### Customising the pipeline inputs

Edit `deploy/tekton/triggers/event-listener.yaml` and update the `TriggerTemplate` hardcoded params:

```yaml
# In the TriggerTemplate resourcetemplates > spec > params section:
- name: image-repository
  value: registry.example.com/quorum        # ← your registry/image name

- name: gitops-repo-url
  value: git@github.com:your-org/quorum-gitops.git  # ← your GitOps repo

- name: gitops-revision
  value: main

- name: kustomize-overlay-path
  value: deploy/kustomize/overlays/production
```

#### Manually triggering a pipeline run

Useful for testing the pipeline without a webhook:

```sh
kubectl create -f - <<EOF
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  generateName: quorum-manual-
  namespace: tekton-pipelines
spec:
  pipelineRef:
    name: quorum-build
  params:
    - name: source-repo-url
      value: git@github.com:your-org/quorum.git
    - name: source-revision
      value: main
    - name: image-repository
      value: registry.example.com/quorum
    - name: gitops-repo-url
      value: git@github.com:your-org/quorum-gitops.git
    - name: gitops-revision
      value: main
    - name: kustomize-overlay-path
      value: deploy/kustomize/overlays/production
  workspaces:
    - name: source
      volumeClaimTemplate:
        spec:
          accessModes: [ReadWriteOnce]
          resources:
            requests:
              storage: 1Gi
    - name: gitops
      volumeClaimTemplate:
        spec:
          accessModes: [ReadWriteOnce]
          resources:
            requests:
              storage: 256Mi
    - name: docker-credentials
      secret:
        secretName: registry-credentials
    - name: git-credentials
      secret:
        secretName: gitops-ssh-key
EOF

# Watch the run
tkn pipelinerun logs -f -n tekton-pipelines $(kubectl get pipelineruns -n tekton-pipelines --sort-by=.metadata.creationTimestamp -o name | tail -1)
```

#### Rootless Buildah (no privileged containers)

By default, `quorum-buildah-build` runs privileged to use the `overlay` storage driver. To run rootless on nodes with `kernel.unprivileged_userns_clone=1`:

1. In `deploy/tekton/tasks.yaml`, find `quorum-buildah-build` and change:
   - `storageDriver` default from `overlay` → `vfs`
   - Remove `securityContext: privileged: true` from both steps
2. Re-apply: `kubectl apply -f deploy/tekton/tasks.yaml`

VFS is slower than overlay for large images but requires no kernel privileges.

### 4.6 Argo CD GitOps setup

#### Application manifest: Kustomize (default)

`deploy/argocd/application.yaml` points Argo CD at the production Kustomize overlay. Tekton writes the new image SHA into the overlay's `kustomization.yaml` on each build, which Argo CD detects and applies.

Before applying, update the repository URL:

```yaml
# deploy/argocd/application.yaml
spec:
  source:
    repoURL: git@github.com:your-org/quorum-gitops.git   # ← update this
    targetRevision: main
    path: deploy/kustomize/overlays/production
```

```sh
kubectl apply -f deploy/argocd/project.yaml
kubectl apply -f deploy/argocd/application.yaml
```

Argo CD polls the GitOps repository every 3 minutes by default. To sync immediately after a Tekton build:

```sh
argocd app sync quorum
# or via the Argo CD web UI
```

To enable webhook-triggered sync (immediate, no polling delay):

1. In Argo CD settings → Webhooks, note the webhook URL.
2. Add a webhook in your GitOps repository pointing to that URL.

#### Application manifest: Helm (alternative)

Use `deploy/argocd/application-helm.yaml` when you prefer Argo CD to render the Helm chart directly rather than applying a pre-rendered Kustomize overlay. This bypasses the Tekton image-tag update step — you would instead update `image.tag` in `values-production.yaml` and push to the source repo.

```sh
kubectl apply -f deploy/argocd/application-helm.yaml
```

Both Application manifests reference the same AppProject (`quorum`).

#### Sync policy

Both Application manifests are configured with:

```yaml
syncPolicy:
  automated:
    prune: true       # Remove resources deleted from git
    selfHeal: true    # Re-apply if someone manually edits cluster resources
    allowEmpty: false # Never sync to an empty state
  syncOptions:
    - CreateNamespace=true
    - ServerSideApply=true  # Avoids annotation size limits on large ConfigMaps
  retry:
    limit: 3
    backoff:
      duration: 10s
      factor: 2
      maxDuration: 2m
```

`selfHeal: true` means any manual `kubectl edit` or `kubectl patch` on a managed resource will be overwritten on the next sync. Do not make manual changes to resources that Argo CD manages.

### 4.7 Deploy with Helm (non-GitOps alternative)

If you prefer imperative Helm deployments without Argo CD:

```sh
# First install
helm upgrade --install quorum deploy/helm/quorum \
  --namespace quorum \
  --create-namespace \
  -f deploy/helm/quorum/values.yaml \
  -f deploy/helm/quorum/values-production.yaml \
  --set image.tag=<commit-sha>

# Update to a new image tag
helm upgrade quorum deploy/helm/quorum \
  --namespace quorum \
  --reuse-values \
  --set image.tag=<new-commit-sha>

# View deployed values
helm get values quorum -n quorum

# View rendered templates (dry run)
helm template quorum deploy/helm/quorum \
  -f deploy/helm/quorum/values.yaml \
  -f deploy/helm/quorum/values-production.yaml \
  --set image.tag=abc1234

# Rollback to previous release
helm rollback quorum -n quorum

# Rollback to a specific revision
helm history quorum -n quorum
helm rollback quorum 3 -n quorum
```

The Helm chart creates a Secret when `secrets.existingSecret` is empty. Because the Secret has the annotation `helm.sh/resource-policy: keep`, `helm uninstall` does not delete it — the credentials survive chart upgrades and uninstalls. Delete it manually only when decommissioning:

```sh
kubectl delete secret quorum-secrets -n quorum
```

### 4.8 Rolling updates and rollbacks

**Zero-downtime rolling updates** are configured in `deploy/k8s/deployment.yaml`:

```yaml
strategy:
  type: RollingUpdate
  rollingUpdate:
    maxUnavailable: 0   # Never reduce capacity below current replicas
    maxSurge: 1         # Bring up one extra pod before terminating the old one
```

With 2 production replicas, a deploy sequence is:
1. New pod (3 total) starts and passes readiness checks.
2. Old pod (back to 2 total) is terminated.
3. Second new pod starts and passes readiness checks.
4. Second old pod is terminated.

Traffic is only routed to pods that pass the readiness probe (`GET /api/v1/auth/bootstrap` returns non-5xx).

**Rollback via Argo CD:**

```sh
# Roll back to the previous sync (restores previous kustomization.yaml commit)
argocd app rollback quorum

# Or sync to a specific git commit
argocd app sync quorum --revision <git-sha>
```

**Rollback via kubectl (emergency):**

```sh
kubectl rollout undo deployment/quorum -n quorum
kubectl rollout status deployment/quorum -n quorum
```

**Rollback via Helm:**

```sh
helm rollback quorum -n quorum        # previous release
helm rollback quorum 4 -n quorum      # specific revision number
```

### 4.9 Database management

Quorum runs database migrations automatically at startup using an advisory lock. Two instances starting simultaneously will not double-apply migrations — the lock ensures only one runs migrations at a time, the other waits.

**Connecting to the production database:**

```sh
# Port-forward the Postgres pod
kubectl port-forward -n quorum svc/postgres 5432:5432

# In another terminal
psql "postgres://quorum:<pass>@localhost:5432/quorum?sslmode=disable"
```

**Backing up the database:**

```sh
# Dump from the port-forwarded connection
pg_dump "postgres://quorum:<pass>@localhost:5432/quorum" \
  --format=custom \
  --file=quorum-$(date +%Y%m%d).pgdump

# Restore
pg_restore --dbname="postgres://quorum:<pass>@localhost:5432/quorum" \
  quorum-20260513.pgdump
```

For managed databases (Cloud SQL, RDS, Aurora), use the cloud provider's built-in backup and restore features. The `QUORUM_DATABASE_URL` DSN works with any PostgreSQL 16+ instance.

### 4.10 TLS and ingress

The Helm chart and Kustomize base both include an Ingress resource. cert-manager is the recommended TLS provider.

**cert-manager ClusterIssuer (Let's Encrypt):**

```yaml
# Apply once per cluster
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: ops@example.com
    privateKeySecretRef:
      name: letsencrypt-prod
    solvers:
      - http01:
          ingress:
            class: nginx
```

The Ingress annotation `cert-manager.io/cluster-issuer: letsencrypt-prod` on the Quorum Ingress (set in `values.yaml`) causes cert-manager to issue and renew the certificate automatically.

**Custom certificate (bring your own):**

```sh
kubectl create secret tls quorum-tls \
  --cert=fullchain.pem \
  --key=privkey.pem \
  -n quorum
```

Then set `ingress.tls[0].secretName: quorum-tls` and remove the cert-manager annotation from `values.yaml`.

### 4.11 Network policy

The `NetworkPolicy` resource in `deploy/k8s/networkpolicy.yaml` (and the Helm template) restricts all traffic to only what Quorum needs:

**Ingress (allowed inbound):**
- Pods in the `ingress-nginx` namespace on `TCP:8080`

**Egress (allowed outbound):**
- DNS: `UDP:53` and `TCP:53` (unrestricted by IP — required for Kubernetes DNS)
- PostgreSQL: pods matching `postgresqlPodSelector` on `TCP:5432`
- SMTP: any destination on `TCP:<smtpPort>` (only when `smtpEnabled: true`)
- HTTPS: any destination on `TCP:443` (for Stripe/PayPal webhook verification)

All other ingress and egress is denied by default.

If your PostgreSQL runs as a managed service (not a pod), replace the `podSelector` egress rule with a `ipBlock` rule:

```yaml
egress:
  - to:
      - ipBlock:
          cidr: 10.0.0.5/32   # your managed DB IP
    ports:
      - port: 5432
        protocol: TCP
```

### 4.12 Observability

Quorum does not ship a metrics endpoint or structured log formatter by default. Recommended additions:

**Logs** — The app writes to stdout/stderr. Collect with:
- Kubernetes: `kubectl logs` / Loki + Promtail
- Cloud: Cloud Logging (GKE), CloudWatch Logs (EKS), Azure Monitor (AKS)

**Metrics** — Add Prometheus instrumentation by wrapping the chi router with a metrics middleware (e.g. `github.com/riandyrn/otelchi` or a custom `promhttp` handler). Expose on a separate port (e.g. `:9090/metrics`) and add a `ServiceMonitor` if using kube-prometheus-stack.

**Tracing** — The chi router is compatible with OpenTelemetry middleware. Add `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` and configure an OTLP exporter.

**Health checks** — Both liveness and readiness probes hit `GET /api/v1/auth/bootstrap`. This endpoint returns `200` (pre-bootstrap) or `403` (bootstrapped) — both are non-5xx and indicate the server is healthy. The probe uses `failureThreshold: 3` with `periodSeconds: 15` for liveness, giving 45 seconds before a pod is restarted.

---

## 5. Secrets management deep-dive

| Secret | Where used | How to generate |
|---|---|---|
| `QUORUM_JWT_SECRET` | JWT signing | `openssl rand -hex 32` |
| `QUORUM_DATABASE_URL` | PostgreSQL connection | `postgres://user:pass@host/db?sslmode=require` |
| `QUORUM_SMTP_PASS` | SMTP auth | from your SMTP provider |
| `QUORUM_STRIPE_WEBHOOK_SECRET` | Stripe HMAC verification | Stripe dashboard → Webhooks → signing secret |
| `QUORUM_PAYPAL_WEBHOOK_ID` | PayPal signature verification | PayPal developer dashboard → Webhooks |
| `registry-credentials` | Tekton image push | `podman login <registry>` → auth.json |
| `gitops-ssh-key` | Tekton GitOps push | `ssh-keygen -t ed25519` → deploy key |
| `github-webhook-secret` | Tekton HMAC validation | `openssl rand -hex 32` |

**JWT secret rotation:**

Rotating `QUORUM_JWT_SECRET` immediately invalidates all active access tokens. Refresh tokens are stored in the database and are validated separately — rotating the JWT secret does not revoke refresh tokens, but any refresh will issue a new access token signed with the new key. Users see a brief "unauthorized" error and are re-authenticated on the next page load.

To rotate without user disruption, deploy a two-key validation approach (not currently implemented — file an issue if needed).

**Database password rotation:**

1. Update `QUORUM_DATABASE_URL` in your secrets manager.
2. Trigger a rolling restart: `kubectl rollout restart deployment/quorum -n quorum`
3. The new pods pick up the new DSN. Old pods drain and terminate.

**Refresh token revocation (all sessions):**

All refresh tokens are stored hashed in the `refresh_tokens` table. To revoke all sessions for a user (e.g., after a suspected compromise):

```sql
DELETE FROM refresh_tokens WHERE user_id = '<uuid>';
```

---

## 6. Container image management

Images are built by Buildah in the Tekton pipeline and pushed with two tags:

| Tag | Example | Purpose |
|---|---|---|
| Commit SHA | `registry.example.com/quorum:abc1234f` | Immutable — pinned in the GitOps overlay |
| `latest` | `registry.example.com/quorum:latest` | Convenience tag for local pulls |

The production Kustomize overlay always pins the commit SHA tag. The `latest` tag is never used in production to ensure deploys are reproducible and auditable.

**Building locally with Podman:**

```sh
# Build with default tag (localhost/quorum:dev)
make pod-build

# Build with a specific registry tag
IMAGE=registry.example.com/quorum:abc1234 make pod-build

# Push
IMAGE=registry.example.com/quorum:abc1234 make pod-push

# Run the built image locally
make pod-run
# Equivalent to:
# podman run --rm -p 127.0.0.1:8080:8080 --env-file .env localhost/quorum:dev
```

**Image security:**

The Dockerfile/Containerfile should produce a minimal image:
- Non-root user (`USER 1000`)
- Read-only root filesystem (mount `/tmp` as `emptyDir`)
- No shell in the final stage if using a distroless or scratch base

The Helm chart and Kustomize base both enforce `readOnlyRootFilesystem: true`, `runAsNonRoot: true`, and `capabilities.drop: [ALL]` via the pod and container security contexts. Quorum writes nothing to disk at runtime — all state is in PostgreSQL.

---

## 7. Troubleshooting

### App fails to start: "dial error" or "connection refused" (database)

The app exits immediately if it cannot connect to PostgreSQL. Check:

```sh
# Verify the secret is mounted correctly
kubectl exec -n quorum deployment/quorum -- env | grep QUORUM_DATABASE_URL

# Test the connection from inside the pod
kubectl exec -n quorum deployment/quorum -- \
  sh -c 'PGPASSWORD=... psql "$QUORUM_DATABASE_URL" -c "SELECT 1"'
```

Common causes:
- Wrong hostname in the DSN (use the Kubernetes service name, e.g. `postgres.quorum.svc.cluster.local`)
- NetworkPolicy blocking the egress to PostgreSQL — check the pod selector matches the Postgres pod's labels
- Secret not created yet in the namespace

### Tekton task fails: "unauthorized" when pushing image

```sh
# Check the secret exists and has the right key
kubectl get secret registry-credentials -n tekton-pipelines -o jsonpath='{.data}' | jq 'keys'
# Should show: [".dockerconfigjson"]

# Verify the auth file has an entry for your registry
kubectl get secret registry-credentials -n tekton-pipelines \
  -o jsonpath='{.data.\.dockerconfigjson}' | base64 -d | jq '.auths | keys'
```

Rebuild the secret from a fresh `podman login`:

```sh
podman login registry.example.com
kubectl delete secret registry-credentials -n tekton-pipelines
kubectl create secret generic registry-credentials \
  --namespace tekton-pipelines \
  --from-file=.dockerconfigjson=$HOME/.config/containers/auth.json \
  --type=kubernetes.io/dockerconfigjson
```

### Tekton task fails: SSH permission denied (GitOps push)

```sh
# Test the deploy key
ssh -T git@github.com -i /tmp/quorum-gitops-deploy
# Should respond: "Hi your-org/quorum-gitops! You've successfully authenticated"
```

Verify the public key is added as a Deploy Key with **Write** access in the GitOps repository settings.

### Argo CD shows "OutOfSync" but won't auto-sync

Common causes:
- The Application has `automated.selfHeal` disabled — check the Application spec.
- There is a sync error in the Argo CD UI — expand the app to see the error message.
- The GitOps repo URL in `application.yaml` does not match the URL in `project.yaml` `sourceRepos`.

```sh
# Manually sync
argocd app sync quorum

# View sync status
argocd app get quorum
```

### Pods in CrashLoopBackOff

```sh
# View recent logs (including from crashed containers)
kubectl logs -n quorum deployment/quorum --previous

# Describe the pod for event details
kubectl describe pod -n quorum -l app.kubernetes.io/name=quorum
```

Common causes:
- Missing or malformed `QUORUM_JWT_SECRET` (must be set; config validation exits on startup)
- Database unreachable (exits immediately — see database section above)
- `QUORUM_SMTP_PORT` set to a non-integer (config validation exits on startup)

### Bootstrap endpoint returns 500

The database connection is healthy but migrations may have failed. Check:

```sh
kubectl logs -n quorum deployment/quorum | grep -i migration
```

If migrations are stuck (advisory lock held), the previous pod likely crashed mid-migration. The lock is session-scoped and released when the connection closes. Force-release:

```sql
-- Connect to the database directly
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE application_name = 'quorum-migrate';
```

Restart the pod after releasing the lock: `kubectl rollout restart deployment/quorum -n quorum`.

### Rate limiter or memory issues under load

The in-process sliding-window rate limiter stores per-IP buckets in memory. Under sustained load from many unique IPs, memory usage will grow. The limiter cleans up empty buckets automatically after the window expires.

If memory is a concern at scale, move rate limiting to the ingress controller (nginx annotation `nginx.ingress.kubernetes.io/limit-rps`) and remove the in-process limiter, or deploy a Redis-backed limiter.
