FROM golang:1.27.1-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY rag ./rag
COPY contracts ./contracts
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/rag ./rag/cmd/rag

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/rag /rag
EXPOSE 8080
ENTRYPOINT ["/rag"]
