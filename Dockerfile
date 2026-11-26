FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

WORKDIR /src

# Install CA certificates so they are available if switching runtime bases
RUN apk add --no-cache ca-certificates tzdata

# Cache Go module downloads separately from source changes
COPY go.mod go.sum ./
RUN go mod download

# Copy application source, migrations, and embedded schemas/templates
COPY . .

# Target SnapDeploy's ARM64 architecture with stripped debug symbols
ARG TARGETOS=linux
ARG TARGETARCH=arm64

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/expensedrift-server ./cmd/server && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/expensedrift-collector ./cmd/collector

# ── Stage 2: Minimal distroless runtime (< 20 MB total image) ─────────────────
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

# Copy Mozilla CA certificates for outbound TLS (D1, B2/Tigris scraping)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy compiled static binaries and default seed file
COPY --from=builder /out/expensedrift-server /app/expensedrift-server
COPY --from=builder /out/expensedrift-collector /app/expensedrift-collector
COPY --from=builder /src/schemas/saas_seeds.json /app/schemas/saas_seeds.json

# SnapDeploy injects PORT (default 8080); run as unprivileged nonroot user (UID 65532)
ENV PORT=8080
EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/app/expensedrift-server"]