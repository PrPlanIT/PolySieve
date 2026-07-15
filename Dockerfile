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

# ---- Runtime image ----
FROM docker.io/library/alpine:3.24.1

LABEL org.opencontainers.image.title="PolySieve" \
      org.opencontainers.image.description="Derive Kubernetes network policy from a GitOps repository's own manifests." \
      org.opencontainers.image.source="https://github.com/PrPlanIT/PolySieve" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.vendor="PrPlanIT"

# kustomize — PolySieve renders the repo by shelling out to it.
ARG KUSTOMIZE_VERSION=v5.8.0
RUN apk add --no-cache ca-certificates && \
    wget -qO- "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2F${KUSTOMIZE_VERSION}/kustomize_${KUSTOMIZE_VERSION}_linux_amd64.tar.gz" \
      | tar -xz -C /usr/local/bin kustomize

COPY --from=builder /out/polysieve /usr/local/bin/polysieve

ENTRYPOINT ["/usr/local/bin/polysieve"]
