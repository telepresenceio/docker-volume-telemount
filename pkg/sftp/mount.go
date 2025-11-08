package sftp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/docker/go-plugins-helpers/volume"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

const (
	mountUndefined = iota
	mountMounted
	mountDisconnected
	mountUnmounted
	mountError
)

// mount is shared between volumeMounts.
type remoteMount struct {
	cancel     func(mount *remoteMount)
	mountPoint string
	host       string
	port       uint16
	readOnly   bool
	state      int32
	volumes    map[string]*volumeDir
	proc       *os.Process
}

func newRemoteMount(mountPoint, host string, port uint16, readOnly bool, cancel func(mount *remoteMount)) *remoteMount {
	return &remoteMount{
		state:      mountUndefined,
		mountPoint: mountPoint,
		host:       host,
		port:       port,
		readOnly:   readOnly,
		volumes:    make(map[string]*volumeDir),
		cancel:     cancel,
	}
}

func (m *remoteMount) String() string {
	return fmt.Sprintf("port=%d, mountPoint=%s, readOnly=%t", m.port, m.mountPoint, m.readOnly)
}

func (m *remoteMount) addVolume(name, dir string) {
	m.volumes[name] = &volumeDir{
		remoteMount: m,
		remoteDir:   dir,
		createdAt:   time.Now(),
	}
}

func (m *remoteMount) getVolume(name string) (*volumeDir, bool) {
	v, ok := m.volumes[name]
	return v, ok
}

func (m *remoteMount) deleteVolume(name string) {
	delete(m.volumes, name)
	if nv := len(m.volumes); nv == 0 {
		m.unmountRemote()
	} else {
		log.Debugf("keeping remote mount, %d volumes are still mounted", nv)
	}
}

func (m *remoteMount) perhapsMountRemote() error {
	if atomic.LoadInt32(&m.state) == mountMounted {
		return nil
	}
	return m.mountRemote()
}

// telAppExports is the directory where the remote traffic-agent's SFTP server exports the
// intercepted container's volumes.
const telAppExports = "/tel_app_exports"

// trapConnectionLost will detect when the sshfs loses its connection. Sadly, it will just hang
// forever when that happens, so we have to kill it.
func trapConnectionLost(cmd *exec.Cmd, cancel context.CancelFunc) {
	rd, wr := io.Pipe()
	cmd.Stdout = wr
	cmd.Stderr = wr
	go func() {
		rdr := bufio.NewReader(rd)
		for {
			line, err := rdr.ReadString('\n')
			ll := len(line)
			if ll > 0 {
				if err == nil {
					// ReadString returns err != nil if and only if the returned data does not end in delim.
					ll--
					if ll == 0 {
						continue
					}
					line = line[:ll]
				}
				log.Debugf("sshfs: %s", line)
				if strings.Contains(line, "remote host has disconnected") || strings.Contains(line, "failed to connect") {
					// In some cases, it actually will quit gracefully, so give it some before killing it.
					time.AfterFunc(200*time.Millisecond, cancel)
				}
			}
			if err != nil {
				break
			}
		}
	}()
}

func (m *remoteMount) mountRemote() error {
	err := os.MkdirAll(m.mountPoint, 0o777)
	if err != nil {
		return fmt.Errorf("failed to create mountpoint directory %s: %v", m.mountPoint, err)
	}
	ip, err := netip.ParseAddr(m.host)
	if err != nil {
		return fmt.Errorf("failed to parse host address %s: %v", m.host, err)
	}
	ap := netip.AddrPortFrom(ip, m.port)

	useIPv6 := ip.Is6()
	sshfsArgs := []string{
		"-F", "none", // don't load the user's config file
		"-f", // don't fork',

		// connection settings
		"-C", // compression
		"-o", "ConnectTimeout=10",

		"-o", "ServerAliveInterval=5",

		// mount directives
		"-o", "follow_symlinks",
		"-o", "auto_unmount",
		"-o", "allow_root", // needed to make --docker-run work as docker runs as root
	}
	if m.readOnly {
		sshfsArgs = append(sshfsArgs, "-o", "ro")
	}

	if useIPv6 {
		// Must use stdin/stdout because sshfs is not capable of connecting with IPv6
		// when using "-o directport=<port>".
		// See https://github.com/libfuse/sshfs/issues/335
		sshfsArgs = append(sshfsArgs,
			"-o", "slave",
			fmt.Sprintf("localhost:%s", telAppExports),
			m.mountPoint, // where to mount it
		)
	} else {
		sshfsArgs = append(sshfsArgs,
			"-o", fmt.Sprintf("directport=%d", ap.Port()),
			fmt.Sprintf("%s:%s", ap.Addr().String(), telAppExports), // what to mount
			m.mountPoint,                                            // where to mount it
		)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sshfs", sshfsArgs...)
	log.Debug(cmd)

	// Get the current Ino and Dev of the mountPoint directory
	st, err := statWithTimeout(ctx, m.mountPoint, 10*time.Millisecond)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to stat mount point %q: %v", m.mountPoint, err)
	}

	// Die if this process dies
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var conn net.Conn
	if useIPv6 {
		conn, err = net.DialTimeout("tcp", ap.String(), 5*time.Second)
		if err != nil {
			cancel()
			return fmt.Errorf("failed to connect to sftp server at %s: %v", ap, err)
		}
		cmd.Stdin = conn
		cmd.Stdout = conn
	}
	log.Debugf("creating %s", m)
	if err = cmd.Start(); err != nil {
		atomic.StoreInt32(&m.state, mountError)
		cancel()
		if conn != nil {
			_ = conn.Close()
		}
		return fmt.Errorf("failed to start sshfs to mount %s: %w", m.mountPoint, err)
	}
	trapConnectionLost(cmd, cancel)
	m.proc = cmd.Process

	sshfsDone := make(chan error, 2)
	go func() {
		m.sshfsWait(cmd, sshfsDone)
		if conn != nil {
			_ = conn.Close()
		}
	}()

	err = m.detectSshfsStarted(ctx, st, sshfsDone)
	if err != nil {
		atomic.StoreInt32(&m.state, mountError)
		_ = m.proc.Kill()
		return err
	}
	atomic.StoreInt32(&m.state, mountMounted)
	return nil
}

func (m *remoteMount) sshfsWait(cmd *exec.Cmd, sshfsDone chan<- error) {
	err := cmd.Wait()
	m.cancel(m)
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
	sshfsDone <- err
}

func (m *remoteMount) detectSshfsStarted(ctx context.Context, st *unix.Stat_t, sshfsDone <-chan error) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	for {
		select {
		case err := <-sshfsDone:
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

func (m *remoteMount) unmountRemote() {
	defer func() {
		if err := os.RemoveAll(m.mountPoint); err != nil {
			log.Errorf("failed to remove mountpoint %s: %v", m.mountPoint, err)
		}
	}()
	if atomic.CompareAndSwapInt32(&m.state, mountMounted, mountUnmounted) {
		log.Debugf("unmounting %s with fusermount3 -u and killing sshfs", m)
		_ = exec.Command("fusermount3", "-u", m.mountPoint).Run()
		_ = m.proc.Kill()
	} else {
		log.Debugf("sshfs on %s is not running", m.mountPoint)
	}
}

func (m *remoteMount) appendVolumes(vols []*volume.Volume) []*volume.Volume {
	for k, v := range m.volumes {
		vols = append(vols, v.asVolume(k))
	}
	return vols
}

func (m *remoteMount) pingHost(timeout time.Duration) error {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(m.host, strconv.Itoa(int(m.port))), timeout)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}
