# Stage 1: Build
FROM golang:1.23-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test ./...
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /solar-ev-charger ./cmd/server

# Stage 2: Runtime
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /solar-ev-charger /solar-ev-charger
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/solar-ev-charger"]
