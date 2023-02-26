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
RUN apk update && apk add tini sshfs
COPY --from=builder /go/bin/docker-volume-telemount /bin/
