FROM golang:1.23.3-alpine@sha256:c694a4d291a13a9f9d94933395673494fc2cc9d4777b85df3a7e70b3492d3574 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal/ ./internal/
ARG KUBEREEL_VERSION=dev
ARG KUBEREEL_REVISION=
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-X github.com/tnqbao/kubereel/internal/api/controller.BuildVersion=${KUBEREEL_VERSION} -X github.com/tnqbao/kubereel/internal/api/controller.BuildRevision=${KUBEREEL_REVISION}" -o /out/kubereel .

FROM debian:bookworm-slim@sha256:e67eeead736ff7bbe258ef088bff683635c561b1fcfefad0806c1df4ab49ce0e
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates ffmpeg=7:5.1.9-0+deb12u1 wget \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 kubereel \
    && useradd --system --uid 10001 --gid kubereel --home-dir /data kubereel \
    && install -d -o kubereel -g kubereel -m 0700 /data
COPY --from=build /out/kubereel /usr/local/bin/kubereel
USER 10001:10001
WORKDIR /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["wget", "--quiet", "--spider", "http://127.0.0.1:8080/readyz"]
ENTRYPOINT ["/usr/local/bin/kubereel"]
