# syntax=docker/dockerfile:1.7
FROM node:24-alpine AS console
WORKDIR /src
COPY web/package.json web/package-lock.json* ./web/
RUN --mount=type=cache,target=/root/.npm cd web && npm ci
COPY web ./web
RUN cd web && npm run build

FROM golang:1.27-alpine AS build
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
# Explicit source allowlist keeps local configuration out of intermediate images.
COPY cmd ./cmd
COPY api ./api
COPY gen ./gen
COPY internal ./internal
COPY db ./db
COPY examples ./examples
COPY --from=console /src/internal/webui/dist ./internal/webui/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/yuanci/yuanci/internal/buildinfo.Version=${VERSION} -X github.com/yuanci/yuanci/internal/buildinfo.Commit=${COMMIT} -X github.com/yuanci/yuanci/internal/buildinfo.Date=${BUILD_DATE}" -o /out/yuanci-server ./cmd/yuanci-server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/yuanci-runner ./cmd/yuanci-runner && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/yuancictl ./cmd/yuancictl

FROM build AS verification
RUN apk add --no-cache build-base

FROM alpine:3.21 AS server
RUN apk add --no-cache ca-certificates tzdata wget && addgroup -S yuanci && adduser -S -G yuanci -u 10001 yuanci
COPY --from=build /out/yuanci-server /usr/local/bin/yuanci-server
COPY --from=build /out/yuancictl /usr/local/bin/yuancictl
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/yuanci-server"]

FROM docker:28-cli AS runner
RUN addgroup -S yuanci && adduser -S -G yuanci -u 10001 yuanci
COPY --from=build /out/yuanci-runner /usr/local/bin/yuanci-runner
# Docker socket group IDs vary by host. Configure group_add in Compose instead of running privileged.
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/yuanci-runner"]

FROM alpine:3.21 AS cli
RUN addgroup -S yuanci && adduser -S -G yuanci -u 10001 yuanci && mkdir /keys && chown 10001:10001 /keys
COPY --from=build /out/yuancictl /usr/local/bin/yuancictl
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/yuancictl"]
