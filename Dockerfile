# The build stage runs on the builder's own platform and cross-compiles for
# the one asked for (linux/arm64 for Fargate), so an amd64 machine building
# the arm64 image does not run the Go toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine AS build
ARG TARGETOS
ARG TARGETARCH
# The release, the image's tag, that logs and traces carry as service_version.
ARG VERSION=

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X github.com/Shubham04-oss/oryxa-go/internal/observability.version=${VERSION}" \
    -o /out/oryxa ./cmd/oryxa

# Every service in one static executable. ORYXA_SERVICES picks which of them
# a container runs; all of them by default.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/oryxa /oryxa
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=30s CMD ["/oryxa", "healthcheck"]
ENTRYPOINT ["/oryxa"]
