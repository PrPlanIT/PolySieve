# ---- Go build stage ----
FROM docker.io/library/golang:1.26.5-alpine3.23 AS builder

WORKDIR /src

# Module download — cached independently of source changes.
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 go build \
      -ldflags "-s -w \
        -X github.com/PrPlanIT/PolySieve/internal/cli.version=${VERSION} \
        -X github.com/PrPlanIT/PolySieve/internal/cli.commit=${COMMIT} \
        -X github.com/PrPlanIT/PolySieve/internal/cli.buildDate=${BUILD_DATE}" \
      -o /out/polysieve ./cmd/polysieve

# kustomize — built from source through the module proxy (same toolchain, no GitHub-release
# egress). Pinned to the version the dungeon host renders with, so output stays byte-exact.
ARG KUSTOMIZE_VERSION=v5.8.0
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 go install sigs.k8s.io/kustomize/kustomize/v5@${KUSTOMIZE_VERSION}

# ---- Runtime image ----
FROM docker.io/library/alpine:3.24.1

LABEL org.opencontainers.image.title="PolySieve" \
      org.opencontainers.image.description="Derive Kubernetes network policy from a GitOps repository's own manifests." \
      org.opencontainers.image.source="https://github.com/PrPlanIT/PolySieve" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.vendor="PrPlanIT"

RUN apk add --no-cache ca-certificates

COPY --from=builder /out/polysieve /usr/local/bin/polysieve
# kustomize — PolySieve renders the repo by shelling out to it (built in the Go stage).
COPY --from=builder /go/bin/kustomize /usr/local/bin/kustomize
# kubectl — for best-effort `--cluster` augmentation. Sourced from the sanctioned alpine/k8s
# image (musl-compatible, pulled through the registry mirror — no dl.k8s.io egress).
COPY --from=docker.io/alpine/k8s:1.34.0 /usr/bin/kubectl /usr/local/bin/kubectl

ENTRYPOINT ["/usr/local/bin/polysieve"]
