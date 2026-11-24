FROM golang:1.23.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go runtime.go workflow_ui.go ./
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -o /out/kubereel .

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates wget \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 goflow \
    && useradd --system --uid 10001 --gid goflow --home-dir /data goflow \
    && install -d -o goflow -g goflow -m 0700 /data
COPY --from=build /out/kubereel /usr/local/bin/kubereel
USER 10001:10001
WORKDIR /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["wget", "--quiet", "--spider", "http://127.0.0.1:8080/readyz"]
ENTRYPOINT ["/usr/local/bin/kubereel"]
