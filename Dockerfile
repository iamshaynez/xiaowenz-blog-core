# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO disabled: modernc.org/sqlite is pure Go → fully static binary.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/core ./cmd/core

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata wget \
    && adduser -D -u 10001 core
COPY --from=build /out/core /usr/local/bin/core
RUN mkdir -p /data && chown core:core /data
USER core
VOLUME /data
EXPOSE 8080
ENV CORE_ADDR=:8080 CORE_DB_PATH=/data/core.db
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/core"]
