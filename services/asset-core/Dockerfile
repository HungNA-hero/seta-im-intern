FROM golang:1.26-alpine AS dependencies
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

FROM dependencies AS dev
RUN go install github.com/air-verse/air@v1.63.0
COPY . .
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=5 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
CMD ["air", "-c", ".air.toml"]

FROM dependencies AS build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/asset-core ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/asset-delete-worker ./cmd/asset-delete-worker

FROM alpine:3.23 AS prod
RUN addgroup -S asset && adduser -S -G asset -H -s /sbin/nologin asset
WORKDIR /app
COPY --from=build --chown=asset:asset /out/asset-core /app/asset-core
COPY --from=build --chown=asset:asset /out/asset-delete-worker /app/asset-delete-worker
USER asset
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=5 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["/app/asset-core"]
