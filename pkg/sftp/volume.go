package sftp

import (
	"path/filepath"
	"time"

	"github.com/docker/go-plugins-helpers/volume"
)

type volumeDir struct {
	*mount
	remoteDir string
	usedBy    []string
	createdAt time.Time
}

func (v *volumeDir) logicalMountPoint() string {
	return filepath.Join(v.mountPoint, v.remoteDir)
}

func (v *volumeDir) asVolume(name string) *volume.Volume {
	return &volume.Volume{
		Name:       name,
		Mountpoint: v.logicalMountPoint(),
		CreatedAt:  v.createdAt.Format(time.RFC3339),
	}
}
