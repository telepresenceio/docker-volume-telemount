package main

import (
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

	if err := volume.NewHandler(sftp.NewDriver()).ServeUnix(pluginSocket, 0); err != nil {
		log.Fatal(err)
	}
}
