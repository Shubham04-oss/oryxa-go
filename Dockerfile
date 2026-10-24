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
# The integrations service's connectors, one binary each, named by its
# directory, which is its manifest id (INTEGRATIONS_CONNECTORS_DIR). fakeapp is
# for tests and stays out.
RUN mkdir -p /out/connectors && for dir in integrations/connectors/*/; do \
      id=$(basename "$dir"); [ "$id" = fakeapp ] && continue; \
      CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" \
        -o "/out/connectors/$id" "./$dir" || exit 1; \
    done
# Connectors run as their own user, connector (65533), so they cannot read the
# service's memory or environment (#478). /connector-launch, which switches to
# that fixed user, alone holds CAP_SETUID and CAP_SETGID; /oryxa holds only
# CAP_KILL, to stop connectors. Both run as nonroot.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" \
      -o /out/connector-launch ./integrations/host/launch
COPY --from=gcr.io/distroless/static-debian12:nonroot /etc/passwd /etc/group /out/etc/
RUN echo 'connector:x:65533:65533:connector:/nonexistent:/sbin/nologin' >> /out/etc/passwd && \
    echo 'connector:x:65533:' >> /out/etc/group && \
    apk add --no-cache libcap && \
    setcap cap_setgid,cap_setuid=ep /out/connector-launch && setcap cap_kill=ep /out/oryxa

# Every service in one static executable. ORYXA_SERVICES picks which of them
# a container runs; all of them by default.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/oryxa /oryxa
COPY --from=build /out/connectors /connectors
COPY --from=build /out/connector-launch /connector-launch
COPY --from=build /out/etc/passwd /out/etc/group /etc/
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=30s CMD ["/oryxa", "healthcheck"]
ENTRYPOINT ["/oryxa"]
