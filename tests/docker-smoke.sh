#!/bin/sh
set -eu

image=${1:-kubereel:local}

docker run --rm --entrypoint /bin/sh "$image" -eu -c '
    test "$(id -u)" = 10001
    ffmpeg -v error -f lavfi -i color=c=black:s=16x16:r=1 -frames:v 1 -c:v libx264 /data/probe.mp4
    test "$(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of csv=p=0 /data/probe.mp4)" = h264

    printf "%s\n" '\''{"prompt":"Docker smoke test"}'\'' > /data/request.json
    kubereel worker --input /data/request.json --output /data/scene.svg
    kubereel render --input /data/scene.svg --output /data/result.svg
    cmp /data/scene.svg /data/result.svg

    kubereel serve &
    server=$!
    trap '\''kill "$server" 2>/dev/null || true'\'' EXIT
    wget --quiet --spider --retry-connrefused --tries=10 --waitretry=1 http://127.0.0.1:8080/readyz
    kill -TERM "$server"
    wait "$server"
    trap - EXIT
'
