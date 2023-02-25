FROM --platform=$BUILDPLATFORM golang:alpine as builder

RUN apk add --no-cache --virtual .build-deps gcc libc-dev
WORKDIR docker-volume-telemount
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /go/bin/docker-volume-telemount

FROM alpine
RUN apk update && apk add sshfs
RUN mkdir -p /run/docker/plugins
COPY --from=builder /go/bin/docker-volume-telemount .
# Tini to reap orphaned child procceses
# Add Tini
RUN apk add tini
ENTRYPOINT ["/sbin/tini", "--"]
CMD ["docker-volume-telemount"]
