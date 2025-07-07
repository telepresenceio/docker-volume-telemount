# Docker volume plugin for Telepresence

This Docker plugin enables the creation of Docker volumes for remote folders published by a Telepresence Traffic Agent
during intercepts.

## Architecture

The plugin is specifically designed to enable remote volumes for the duration of an intercept. It uses an
[sshfs](https://man7.org/linux/man-pages/man1/sshfs.1.html) client internally and connects to the Traffic Agent's SFTP
server via a port that is exposed by the Telepresence container based daemon. The port is only reachable from the docker
internal network.

On macOS and Windows platforms, the volume driver runs in the Docker VM, so no installation of sshfs or a platform specific
FUSE implementations such as macFUSE or WinFSP are needed. The `sshfs` client is already installed in the Docker VM.

## Usage

### Install

The `latest` tag is an alias for `amd64`, so if you are using that architecture, you can install it using:

```console
$ docker plugin install datawire/telemount --alias telemount
```
You can also install using the architecture tag (currently `amd64` or `arm64`):
```console
$ docker plugin install datawire/telemount:arm64 --alias telemount
```

### Intercept and create volumes

Connect in docker mode and then intercept with `--docker-run`. The mounts will automatically use this plugin:
```
$ telepresence connect --docker
$ telepresence intercept echo-easy --docker-run -- busybox ls ls /var/run/secrets/kubernetes.io/serviceaccount
```

#### More detailed (not using --docker and --docker-run)

Create an intercept. Use `--local-mount-port 1234` to set up a bridge instead of mounting, and `--detailed-ouput --output yaml` so that
the command outputs the environment in a readable form:
```console
$ telepresence connect
$ telepresence intercept --local-mount-port 1234  --port 8080 --detailed-output --output yaml echo-easy
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

## Debugging

Start by configuring telepresence to not check for the latest version of the plugin, but instead use our debug version by
adding the following yaml to the `config.yml` (on Linux, this will be in `~/.config/telepresence/config.yml`, and on mac
you'll find it in `"$HOME/Library/Application Support/telepresence/config.yml"`:
```yaml
docker:
  telemount:
    tag: debug
```

Build the plugin for debugging. The command both builds and enables the plugin:
```console
$ make debug
```

Figure out the ID of the plugin:
```console
$ PLUGIN_ID=`docker plugin list --no-trunc -f capability=volumedriver -f enabled=true -q`
```
and start viewing what it prints on /var/log/telemount.log.
```
$ sudo runc --root /run/docker/runtime-runc/plugins.moby exec $(PLUGIN_ID) tail -n 400 -f /var/log/telemount.log
```

Now connect telepresence with `--docker` and do an intercept with `--docker-run`.

## Credits
To the [Rclone project](https://github.com/rclone/rclone) project and [PR 5668](https://github.com/rclone/rclone/pull/5668)
specifically for showing a good way to create multi-arch plugins.
To the [Docker volume plugin for sshFS](https://github.com/vieux/docker-volume-sshfs) for providing a good example of a
Docker volume plugin.