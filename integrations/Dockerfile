FROM golang:1.27.1-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY integrations ./integrations
COPY contracts ./contracts
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/integrations ./integrations/cmd/integrations

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/integrations /integrations
EXPOSE 8082
ENTRYPOINT ["/integrations"]
