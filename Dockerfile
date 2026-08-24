FROM golang:1.25-alpine AS build

RUN apk add --no-cache ca-certificates

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /javbeacon \
    ./cmd/javbeacon


FROM alpine:3.22

RUN apk add --no-cache \
    bash \
    curl \
    ca-certificates \
    && addgroup -S javbeacon \
    && adduser -S -G javbeacon javbeacon

WORKDIR /app

COPY --from=build /javbeacon /usr/local/bin/javbeacon

RUN mkdir -p /app/data \
    && chown -R javbeacon:javbeacon /app

USER javbeacon

EXPOSE 8080

VOLUME ["/app/data"]

ENTRYPOINT ["/usr/local/bin/javbeacon"]
