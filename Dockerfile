FROM golang:1.27.1-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/oryxa ./cmd/oryxa

# Every service in one static executable. ORYXA_SERVICES picks which of them
# a container runs; all of them by default.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/oryxa /oryxa
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=30s CMD ["/oryxa", "healthcheck"]
ENTRYPOINT ["/oryxa"]
