FROM dock.mau.dev/tulir/lottieconverter:alpine-3.19 AS lottie

FROM golang:1-alpine3.20 AS builder

RUN apk add --no-cache git ca-certificates build-base su-exec olm-dev

COPY . /build
WORKDIR /build
RUN go build -o /usr/bin/mautrix-discord

FROM alpine:3.20

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache ffmpeg su-exec ca-certificates olm bash jq yq-go curl \
    zlib libpng giflib libstdc++ libgcc

COPY --from=lottie /usr/lib/librlottie.so* /usr/lib/
COPY --from=lottie /usr/local/bin/lottieconverter /usr/local/bin/lottieconverter
COPY --from=builder /usr/bin/mautrix-discord /usr/bin/mautrix-discord
COPY --from=builder /build/docker-run.sh /docker-run.sh
VOLUME /data

CMD ["/docker-run.sh"]
