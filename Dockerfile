FROM golang:1.25-alpine AS build

ARG VERSION

RUN apk add --no-cache ca-certificates

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w -X github.com/Net005/JAVBeacon/internal/version.Value=${VERSION}" \
    -o /javbeacon \
    ./cmd/javbeacon


FROM alpine:3.22

ARG VERSION

LABEL org.opencontainers.image.title="JAVBeacon" \
      org.opencontainers.image.source="https://github.com/Net005/JAVBeacon" \
      org.opencontainers.image.description="Private, self-hosted JAV release monitoring and acquisition management" \
      org.opencontainers.image.version="${VERSION}"

RUN apk add --no-cache \
    bash \
    curl \
    ca-certificates \
    tzdata \
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
