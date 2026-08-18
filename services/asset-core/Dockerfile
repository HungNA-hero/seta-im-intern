FROM golang:1.26-alpine AS dependencies
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

FROM dependencies AS dev
RUN go install github.com/air-verse/air@v1.63.0
COPY . .
EXPOSE 8080 8081
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=5 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
CMD ["air", "-c", ".air.toml"]

FROM dependencies AS build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/asset-core ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/asset-delete-worker ./cmd/asset-delete-worker \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/media-worker ./cmd/media-worker \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/media-processor ./cmd/media-processor

FROM alpine:3.23 AS prod
RUN addgroup -S asset \
 && adduser -S -G asset -H -s /sbin/nologin asset \
 && addgroup -S -g 65532 media-processor \
 && adduser -S -D -H -u 65532 -G media-processor -s /sbin/nologin media-processor
WORKDIR /app
COPY --from=build --chown=asset:asset /out/asset-core /app/asset-core
COPY --from=build --chown=asset:asset /out/asset-delete-worker /app/asset-delete-worker
COPY --from=build --chown=asset:asset /out/media-worker /app/media-worker
COPY --from=build --chown=root:root /out/media-processor /usr/local/bin/media-processor
RUN chmod 0555 /usr/local/bin/media-processor
USER asset
EXPOSE 8080 8081
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=5 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["/app/asset-core"]
