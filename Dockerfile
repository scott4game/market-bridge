# syntax=docker/dockerfile:1

FROM node:24-alpine AS build-web
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts
COPY web ./web
RUN mkdir -p internal/localclient/ui && npm run build:web

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build-base
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=build-web /src/internal/localclient/ui/formula-worker.js ./internal/localclient/ui/formula-worker.js

FROM build-base AS build-server
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/go-server ./cmd/go-server

FROM build-base AS build-client
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/go-client ./cmd/go-client

FROM alpine:3.21 AS runtime
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 10001 market && \
    adduser -S -D -H -u 10001 -G market market && \
    mkdir -p /data && chown -R market:market /data
USER market:market
WORKDIR /app

FROM runtime AS go-server
COPY --from=build-server /out/go-server /usr/local/bin/go-server
EXPOSE 17601
VOLUME ["/data"]
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --spider -q http://127.0.0.1:17601/healthz || exit 1
ENTRYPOINT ["go-server"]

FROM runtime AS go-client
COPY --from=build-client /out/go-client /usr/local/bin/go-client
EXPOSE 17600
VOLUME ["/data"]
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --spider -q http://127.0.0.1:17600/readyz || exit 1
ENTRYPOINT ["go-client"]
CMD ["serve"]
