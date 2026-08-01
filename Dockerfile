FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /quorum ./cmd/quorum

FROM scratch
COPY --from=builder /quorum /quorum
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
# Run as a non-root user (matches the k8s securityContext runAsUser: 1000).
# The app binds :8080 (unprivileged) and writes nothing to disk, so no
# filesystem ownership or passwd entry is needed in the scratch image.
USER 1000:1000
ENTRYPOINT ["/quorum"]
