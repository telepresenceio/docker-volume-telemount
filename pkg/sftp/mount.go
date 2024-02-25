package sftp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/docker/go-plugins-helpers/volume"
	"golang.org/x/sys/unix"

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
		"-o", "ServerAliveInterval=5",
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
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	ctx := context.Background()

	// Get the current Ino and Dev of the mountPoint directory
	st, err := statWithTimeout(ctx, m.mountPoint, 10*time.Millisecond)

	// Die if this process dies
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	log.Debugf("mounting %s", m)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start sshfs to mount %s: %w", m.mountPoint, err)
	}
	m.proc = cmd.Process
	m.mounted.Store(true)

	m.done = make(chan error, 2)
	starting := atomic.Bool{}
	starting.Store(true)
	go m.sshfsWait(cmd, &starting)

	err = m.detectSshfsStarted(ctx, st)
	if starting.Swap(false) {
		if err != nil {
			m.done <- err
			_ = m.proc.Kill()
		}
	}
	return err
}

func (m *mount) sshfsWait(cmd *exec.Cmd, starting *atomic.Bool) {
	defer close(m.done)
	err := cmd.Wait()
	if err != nil {
		var ex *exec.ExitError
		if errors.As(err, &ex) {
			if len(ex.Stderr) > 0 {
				err = fmt.Errorf("%s: exit status %d", string(ex.Stderr), ex.ExitCode())
			}
		}
		log.Errorf("sshfs exited with %v", err)
	} else {
		log.Debug("sshfs exited normally")
	}

	// Restore to unmounted state
	m.mounted.Store(false)
	if starting.Swap(false) {
		if err != nil {
			m.done <- err
		}
	}
	m.mounted.Store(false)

	// sshfs sometimes leave the mount point in a bad state. This will clean it up
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	go func() {
		defer cancel()
		_ = exec.Command("fusermount", "-uz", m.mountPoint).Run()
	}()
	<-ctx.Done()
}

func (m *mount) detectSshfsStarted(ctx context.Context, st *unix.Stat_t) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	for {
		select {
		case err := <-m.done:
			// sshfs command failed
			return err
		case <-ctx.Done():
			err := ctx.Err()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				err = fmt.Errorf("timeout trying to stat mount point %q", m.mountPoint)
			}
			return err
		case <-ticker.C:
			if mountSt, err := statWithTimeout(ctx, m.mountPoint, 400*time.Millisecond); err == nil {
				if st.Ino != mountSt.Ino || st.Dev != mountSt.Dev {
					// Mount point changed, so we're done here
					log.Debug("mountpoint inode or dev changed")
					return nil
				}
			} else {
				// we don't consider a failure to stat fatal here, just a cause for a retry.
				if !errors.Is(err, context.DeadlineExceeded) {
					log.Errorf("unable to stat mount point %q: %v", m.mountPoint, err)
				}
			}
		}
	}
}

// statWithTimeout performs a normal unix.Stat but will not allow that it hangs for
// more than the given timeout.
func statWithTimeout(ctx context.Context, path string, timeout time.Duration) (*unix.Stat_t, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	errCh := make(chan error, 1)
	mountSt := new(unix.Stat_t)
	go func() {
		errCh <- unix.Stat(path, mountSt)
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-errCh:
		if err != nil {
			return nil, err
		}
		return mountSt, nil
	}
}

func (m *mount) unmountVolume() (err error) {
	defer func() {
		if err := os.RemoveAll(m.mountPoint); err != nil {
			log.Errorf("failed to remove mountpoint %s: %v", m.mountPoint, err)
		}
	}()
	if m.mounted.Load() {
		log.Debug("kindly asking sshfs to stop")
		_ = m.proc.Signal(os.Interrupt)
		select {
		case err = <-m.done:
		case <-time.After(5 * time.Second):
			log.Debug("forcing sshfs to stop")
			_ = m.proc.Kill()
		}
	}
	return err
}

func (m *mount) appendVolumes(vols []*volume.Volume) []*volume.Volume {
	for k, v := range m.volumes {
		vols = append(vols, v.asVolume(k))
	}
	return vols
}
