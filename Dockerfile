# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
# Pure-Go SQLite driver, so the binary is fully static (no cgo).
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/netpulse ./cmd/netpulse \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/netpulse /netpulse
COPY --from=build --chown=nonroot:nonroot /out/data /data
COPY config.example.json /etc/netpulse/config.json
ENV NETPULSE_CONFIG=/etc/netpulse/config.json \
    NETPULSE_DB=/data/netpulse.db
VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD ["/netpulse", "-healthcheck"]
ENTRYPOINT ["/netpulse"]
