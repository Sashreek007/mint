# syntax=docker/dockerfile:1.7

# ---------- Stage 1: build ----------
FROM golang:1.25-alpine AS builder

WORKDIR /src

# Copy go.mod first so dependency download caches independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

# Now copy the rest of the source.
COPY . ./

# Build a static binary. CGO_ENABLED=0 = no C dependencies = portable binary.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/keyservice .

# ---------- Stage 2: runtime ----------
FROM alpine:3.20

# CA certs so we can make outbound HTTPS calls later.
RUN apk add --no-cache ca-certificates

# Run as an unprivileged user, not root.
RUN adduser -D -u 10001 appuser
USER appuser

COPY --from=builder /out/keyservice /usr/local/bin/keyservice

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/keyservice"]
