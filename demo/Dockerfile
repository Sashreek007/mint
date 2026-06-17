# syntax=docker/dockerfile:1.7

# ---------- Stage 1: build ----------
FROM golang:1.25-alpine AS builder
WORKDIR /src

# The demo module uses `replace ... => ../keyservice-go`, so BOTH module trees
# must be in the build context. (No `go mod download`: the SDK is stdlib-only,
# so this consumer has zero external deps to fetch.)
COPY keyservice-go/ ./keyservice-go/
COPY demo/ ./demo/

WORKDIR /src/demo
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/demo .

# ---------- Stage 2: runtime ----------
FROM alpine:3.20
RUN adduser -D -u 10001 appuser
USER appuser
COPY --from=builder /out/demo /usr/local/bin/demo
EXPOSE 9000
ENTRYPOINT ["/usr/local/bin/demo"]
