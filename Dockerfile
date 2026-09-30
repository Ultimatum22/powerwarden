# syntax=docker/dockerfile:1

# Local build of the same image ko publishes in CI (see .ko.yaml).
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o /out/labpower ./cmd/labpower \
 && mkdir /out/state

# distroless nonroot (UID 65532): no shell, just CA certs and the binary.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/labpower /ko-app/labpower
# Owned by nonroot so a fresh named volume mounted here inherits it.
COPY --from=build --chown=65532:65532 /out/state /var/lib/labpower
ENTRYPOINT ["/ko-app/labpower"]
CMD ["serve"]
