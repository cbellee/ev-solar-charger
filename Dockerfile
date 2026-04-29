# syntax=docker/dockerfile:1.7

# Stage 1: build the React/Vite SPA. Output goes to /src/internal/web/dist
# which is then embedded into the Go binary in stage 2.
FROM node:20-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
# Vite inlines VITE_-prefixed env vars at build time. These IDs are public
# (they ship to every browser) so it's safe to set them via build args.
ARG VITE_ENTRA_TENANT_ID
ARG VITE_ENTRA_CLIENT_ID
ENV VITE_ENTRA_TENANT_ID=${VITE_ENTRA_TENANT_ID}
ENV VITE_ENTRA_CLIENT_ID=${VITE_ENTRA_CLIENT_ID}
# Vite writes to ../internal/web/dist (see vite.config.ts).
RUN mkdir -p /src/internal/web && npm run build

# Stage 2: build the Go binary, embedding the SPA bundle.
FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Bring in the freshly built SPA bundle so //go:embed all:dist sees it.
COPY --from=web /src/internal/web/dist ./internal/web/dist
RUN go test ./...
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /solar-ev-charger ./cmd/server
RUN mkdir -p /data && chown 65534:65534 /data

# Stage 3: minimal runtime.
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /solar-ev-charger /solar-ev-charger
COPY --from=builder --chown=nonroot:nonroot /data /data
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/solar-ev-charger"]
