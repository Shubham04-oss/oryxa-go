FROM golang:1.27.1-alpine AS build

# primary is its own Go module. Its go.mod replaces the root module with ../..
# for the shared /internal packages, so the root go.mod and /internal come too.
WORKDIR /src/agents/primary
COPY go.mod /src/
COPY agents/primary/go.mod agents/primary/go.sum ./
RUN go mod download
COPY internal /src/internal
COPY agents/primary ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/primary ./cmd/primary

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/primary /primary
EXPOSE 8080
ENTRYPOINT ["/primary"]
