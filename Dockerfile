# The build stage runs on the builder's own platform and cross-compiles for
# the one asked for (linux/arm64 for Fargate), so an amd64 machine building
# the arm64 image does not run the Go toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
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
# The integrations service's connectors, one binary each, named by its
# directory, which is its manifest id (INTEGRATIONS_CONNECTORS_DIR). fakeapp is
# for tests and stays out.
RUN mkdir -p /out/connectors && for dir in integrations/connectors/*/; do \
      id=$(basename "$dir"); [ "$id" = fakeapp ] && continue; \
      CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" \
        -o "/out/connectors/$id" "./$dir" || exit 1; \
    done

# Every service in one static executable. ORYXA_SERVICES picks which of them
# a container runs; all of them by default.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/oryxa /oryxa
COPY --from=build /out/connectors /connectors
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=30s CMD ["/oryxa", "healthcheck"]
ENTRYPOINT ["/oryxa"]
