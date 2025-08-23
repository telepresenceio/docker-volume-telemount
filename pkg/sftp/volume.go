package sftp

import (
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/docker/go-plugins-helpers/volume"
)

type volumeDir struct {
	*remoteMount
	remoteDir string
	usedBy    []string
	createdAt time.Time
	mounted   atomic.Bool
}

func (v *volumeDir) logicalMountPoint() string {
	return filepath.Join(v.mountPoint, v.remoteDir)
}

func (v *volumeDir) asVolume(name string) *volume.Volume {
	return &volume.Volume{
		Name:       name,
		Mountpoint: v.logicalMountPoint(),
		CreatedAt:  v.createdAt.Format(time.RFC3339),
		Status: map[string]any{
			"host":      v.host,
			"port":      v.port,
			"remoteDir": v.remoteDir,
			"usedBy":    v.usedBy,
		},
	}
}
