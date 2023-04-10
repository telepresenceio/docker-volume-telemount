package sftp

import (
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/docker/go-plugins-helpers/volume"

	"github.com/datawire/docker-volume-telemount/pkg/log"
)

// mount is shared between volumeMounts.
type mount struct {
	mountPoint string
	host       string
	port       uint16
	mounted    atomic.Bool
	done       chan error
	volumes    map[string]*volumeDir
	proc       *os.Process
}

func newMount(mountPoint, host string, port uint16) *mount {
	return &mount{
		mountPoint: mountPoint,
		host:       host,
		port:       port,
		volumes:    make(map[string]*volumeDir),
		done:       make(chan error, 1),
	}
}

func (m *mount) String() string {
	return fmt.Sprintf("port=%d, mountPoint=%s", m.port, m.mountPoint)
}

func (m *mount) addVolume(name, dir string) {
	m.volumes[name] = &volumeDir{
		mount:     m,
		remoteDir: dir,
		createdAt: time.Now(),
	}
}

func (m *mount) getVolume(name string) (*volumeDir, bool) {
	v, ok := m.volumes[name]
	return v, ok
}

func (m *mount) deleteVolume(name string) error {
	delete(m.volumes, name)
	return nil
}

func (m *mount) perhapsMount() error {
	if m.mounted.Load() {
		return nil
	}
	return m.mountVolume()
}

func (m *mount) perhapsUnmount() error {
	for _, v := range m.volumes {
		if len(v.usedBy) > 0 {
			return nil
		}
	}
	return m.unmountVolume()
}

// telAppExports is the directory where the remote traffic-agent's SFTP server exports the
// intercepted container's volumes.
const telAppExports = "/tel_app_exports"

func (m *mount) mountVolume() error {
	err := os.MkdirAll(m.mountPoint, 0o777)
	if err != nil {
		return fmt.Errorf("failed to create mountpoint directory %s: %v", m.mountPoint, err)
	}
	sshfsArgs := []string{
		fmt.Sprintf("%s:%s", m.host, telAppExports), // what to mount
		m.mountPoint, // where to mount it
		"-F", "none", // don't load the user's config file
		"-f",
		// connection settings
		"-C", // compression
		"-o", "ConnectTimeout=10",
		"-o", fmt.Sprintf("directport=%d", m.port),

		// mount directives
		"-o", "follow_symlinks",
		"-o", "allow_root", // needed to make --docker-run work as docker runs as root
	}
	if log.IsDebug() {
		sshfsArgs = append(sshfsArgs, "-d")
	}
	exe := "sshfs"
	cmd := exec.Command(exe, sshfsArgs...)
	done := make(chan error, 1)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Die if this process dies
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	log.Debugf("mounting %s", m)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start sshfs to mount %s: %w", m.mountPoint, err)
	}
	m.proc = cmd.Process
	m.mounted.Store(true)
	m.done = done
	go func() {
		// The Wait here will always exit with an error status, because that's what happens
		// when sshfs gets interrupted.
		err = cmd.Wait()
		// Restore to unmounted state
		m.mounted.Store(false)
		close(done)
		if err == nil {
			log.Debug("sshfs exited normally")
		} else {
			log.Errorf("sshfs exited with %v", err)
		}
	}()

	// Let's wait a short while to check if the command errors.
	select {
	case <-time.After(1 * time.Second):
		// No errors so far. We're probably good.
		log.Debugf("mount successful")
		return nil
	case err := <-done:
		return err
	}
}

func (m *mount) unmountVolume() (err error) {
	defer func() {
		if err := os.RemoveAll(m.mountPoint); err != nil {
			log.Errorf("failed to remove mountpoint %s: %v", m.mountPoint, err)
		}
	}()
	if err := exec.Command("umount", m.mountPoint).Run(); err != nil {
		log.Errorf("failed to unmount volumeDir %s: %v", m.mountPoint, err)
	}
	if m.mounted.Load() {
		log.Debug("kindly asking sshfs to stop")
		//		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-m.done:
		case <-time.After(5 * time.Second):
			log.Debug("forcing sshfs to stop")
			_ = m.proc.Kill()
		}
		return nil
	}
	return nil
}

func (m *mount) appendVolumes(vols []*volume.Volume) []*volume.Volume {
	for k, v := range m.volumes {
		vols = append(vols, v.asVolume(k))
	}
	return vols
}
