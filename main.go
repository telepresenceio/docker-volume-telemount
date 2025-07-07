package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/docker/go-plugins-helpers/volume"
	"github.com/sirupsen/logrus"

	"github.com/telepresenceio/docker-volume-telemount/pkg/log"
	"github.com/telepresenceio/docker-volume-telemount/pkg/sftp"
)

const pluginSocket = "/run/docker/plugins/telemount.sock"
const pluginLog = "/var/log/telemount.log"

func main() {
	logrus.SetFormatter(log.NewFormatter("15:04:05.0000"))
	lf, err := os.Create(pluginLog)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to create plugin log file %s: %s\n", pluginLog, err)
		os.Exit(1)
	}
	logrus.SetOutput(lf)
	if debug, ok := os.LookupEnv("DEBUG"); ok {
		ok, _ = strconv.ParseBool(debug)
		logrus.SetLevel(logrus.DebugLevel)
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	if err := volume.NewHandler(sftp.NewDriver()).ServeUnix(pluginSocket, 0); err != nil {
		logrus.Fatal(err)
	}
}
