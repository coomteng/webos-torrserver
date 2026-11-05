# Build context = repository root. Multi-stage: static Go binary + the TV app and simulator baked in.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY sidecar/go.mod sidecar/go.sum ./
RUN go mod download
COPY sidecar/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /torr-remote .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /torr-remote /usr/local/bin/torr-remote
COPY app /app/tv
COPY sim /app/sim
ENV TORR_SERVER=http://127.0.0.1:8090 \
    TORR_PORT=8791 \
    TORR_APP_DIR=/app/tv \
    TORR_SIM=/app/sim/index.html
EXPOSE 8791
HEALTHCHECK --interval=60s --timeout=5s --retries=3 CMD wget -qO- http://127.0.0.1:8791/health >/dev/null || exit 1
CMD ["torr-remote"]
