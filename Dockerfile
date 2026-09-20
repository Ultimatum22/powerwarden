# syntax=docker/dockerfile:1

# Builds the labpower binary and packages it in a minimal, non-root,
# distroless image. Cross-compiles natively via Go's own toolchain rather
# than emulation, so `docker buildx build --platform linux/amd64,linux/arm64`
# is fast (the recommended pattern for Go multi-arch images).
FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/labpower ./cmd/labpower

# distroless/static: no shell, no package manager, just the binary, CA
# certs (needed for the Proxmox/weather/notify HTTPS calls), and a
# non-root user — the container-native equivalent of the systemd unit's
# DynamicUser + extensive sandboxing in deploy/labpower.service.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/labpower /usr/local/bin/labpower

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/labpower"]
CMD ["serve"]
