# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" \
    -o /out/newapi-checkin ./cmd/checkin

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && mkdir -p /config /logs

ENV TZ=Asia/Shanghai

WORKDIR /app

COPY --from=builder /out/newapi-checkin /usr/local/bin/newapi-checkin

ENTRYPOINT ["/usr/local/bin/newapi-checkin"]
CMD ["-config", "/config/config.yaml", "-log", "/logs/checkin.log"]
