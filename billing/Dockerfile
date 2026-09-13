FROM golang:1.27.1-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY billing ./billing
COPY contracts ./contracts
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/billing ./billing/cmd/billing

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/billing /billing
EXPOSE 8080
ENTRYPOINT ["/billing"]
