package main

import (
	"context"
	"os"
	"strconv"

	"github.com/docker/go-plugins-helpers/volume"

	"github.com/datawire/docker-volume-telemount/pkg/log"
	"github.com/datawire/docker-volume-telemount/pkg/sftp"
)

const pluginSocket = "/run/docker/plugins/telemount.sock"

func main() {
	if debug, ok := os.LookupEnv("DEBUG"); ok {
		ok, _ = strconv.ParseBool(debug)
		log.SetDebug(ok)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log.Start(ctx.Done())

	if err := volume.NewHandler(sftp.NewDriver()).ServeUnix(pluginSocket, 0); err != nil {
		log.Fatal(err)
	}
}
