# Docker volume plugin for Telepresence

This Docker plugin enables the creation of Docker volumes for remote folders published by a Telepresence traffic-agent
during intercepts. On macOS and Windows platforms, the volume driver runs in the Docker VM, so no installation of sshfs
or platform specific FUSE implementations such as macFUSE or WinFSP are needed.

## Usage

### Install

The `latest` tag is an alias for `amd64`, so if you are using that architecture, you can install using:

```console
$ docker plugin install datawire/telemount --alias telemount
```
You can also install using the architecture tag (currently `amd64` or `arm64`):
```console
$ docker plugin install datawire/telemount:arm64 --alias telemount
```

### Intercept and create volumes
Create an intercept. Use `--local-mount-port 1234` to set up a bridge instead of mounting, and `--detailed-ouput --output yaml` so that
the command outputs the environment in a readable form:
```console
$ telepresence intercept --local-mount-port 1234  --port 8080 --http-header who=me --detailed-output --output yaml echo-easy
...
    TELEPRESENCE_CONTAINER: echo-easy
    TELEPRESENCE_MOUNTS: /var/run/secrets/kubernetes.io
...
```
Create a volume that represents the remote mount from the intercepted container (values can be found in environment variables
`TELEPRESENCE_CONTAINER` and `TELEPRESENCE_MOUNTS`):
```console
$ docker volume create -d telemount -o port=1234 -o container=echo-easy -o dir=var/run/secrets/kubernetes.io echo-easy-1
```
Access the volume:
```console
$ docker run --rm -v echo-easy-1:/var/run/secrets/kubernetes.io busybox ls /var/run/secrets/kubernetes.io/serviceaccount
ca.crt
namespace
token
```

## Credits
To the [Rclone project](https://github.com/rclone/rclone) project and [PR 5668](https://github.com/rclone/rclone/pull/5668)
specifically for showing a good way to create multi-arch plugins.
To the [Docker volume plugin for sshFS](https://github.com/vieux/docker-volume-sshfs) for providing a good example of a
Docker volume plugin.