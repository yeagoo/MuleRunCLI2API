# syntax=docker/dockerfile:1.7

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /out/cli2api ./cmd/cli2api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cli2api /usr/local/bin/cli2api
ENV CLI2API_PORT=8080
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/usr/local/bin/cli2api"]
