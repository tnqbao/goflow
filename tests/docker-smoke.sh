#!/bin/sh
set -eu

image=${1:-kubereel:local}

docker run --rm --entrypoint /bin/sh "$image" -eu -c '
    test "$(id -u)" = 10001
    ffmpeg -v error -f lavfi -i color=c=black:s=16x16:r=1 -frames:v 1 -c:v libx264 /data/probe.mp4
    test "$(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of csv=p=0 /data/probe.mp4)" = h264

    printf "%s\n" '\''{"type":"llm","prompt":"Docker smoke test"}'\'' > /data/llm.json
    kubereel worker --input /data/llm.json --output /data/script.json
    grep -Fq "Sample script: Docker smoke test" /data/script.json

    printf "%s\n" '\''{"type":"image","prompt":"Docker smoke test"}'\'' > /data/image.json
    kubereel worker --input /data/image.json --output /data/scene.svg
    grep -Fq "Docker smoke test" /data/scene.svg

    printf "%s\n" '\''{"type":"video","prompt":"Scene 1"}'\'' > /data/video1.json
    printf "%s\n" '\''{"type":"video","prompt":"Scene 2"}'\'' > /data/video2.json
    kubereel worker --input /data/video1.json --output /data/clip1.mp4
    kubereel worker --input /data/video2.json --output /data/clip2.mp4

    printf "%s\n" '\''{"operation":"render_mp4","inputs":["clip1.mp4","clip2.mp4"],"aspectRatio":"9:16","fps":24}'\'' > /data/render.json
    kubereel render --input /data/render.json --output /data/rendered.mp4
    test "$(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name,width,height -of csv=p=0 /data/rendered.mp4)" = "h264,720,1280"

    kubereel serve &
    server=$!
    trap '\''kill "$server" 2>/dev/null || true'\'' EXIT
    wget --quiet --spider --retry-connrefused --tries=10 --waitretry=1 http://127.0.0.1:8080/readyz
    kill -TERM "$server"
    wait "$server"
    trap - EXIT
'
