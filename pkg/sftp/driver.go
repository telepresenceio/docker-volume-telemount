package sftp

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/go-plugins-helpers/volume"
	log "github.com/sirupsen/logrus"
)

type driver struct {
	// All access to the driver is synchronized using this lock
	lock                   sync.RWMutex
	volumePath             string
	remoteMounts           map[string]*remoteMount
	disconnectedLingerTime time.Duration
	hostPingTimeout        time.Duration
}

func logResponse(err error, format string, args ...any) {
	if err == nil {
		log.Debugf(format, args...)
	} else {
		log.Errorf(format+": %v", append(args, err)...)
	}
}

const (
	defaultDisconnectedLingerTime = 5 * time.Second
	defaultHostPingTimeout        = 1 * time.Second
)

func durationEnv(env string, d time.Duration) time.Duration {
	if v, ok := os.LookupEnv(env); ok {
		if dv, err := time.ParseDuration(v); err == nil {
			return dv
		}
	}
	return d
}

// NewDriver creates a new driver that will mount volumes under /mnt/volumes (which is
// the propagated-mount directory that Docker assigns for the driver instance).
func NewDriver() volume.Driver {
	log.Debug("NewDriver")
	volumePath := filepath.Join("/mnt", "volumes")
	d := &driver{
		volumePath:   volumePath,
		remoteMounts: make(map[string]*remoteMount),
	}
	d.disconnectedLingerTime = durationEnv("DISCONNECTED_LINGER_TIME", defaultDisconnectedLingerTime)
	d.hostPingTimeout = durationEnv("HOST_PING_TIMEOUT", defaultHostPingTimeout)
	return d
}

// Create creates a new volume with the given options. Volumes with the
// same ip and port will share the same running sshfs instance. That instance
// is created on demand when the first volume is mounted and removed when there
// are no more mounted volumes.
func (d *driver) Create(r *volume.CreateRequest) (err error) {
	log.Debugf("Create %s, %v", r.Name, r)
	defer func() {
		logResponse(err, "Create %s return", r.Name)
	}()

	var container, dir, host string
	var port uint16
	var readOnly bool
	for key, val := range r.Options {
		switch key {
		case "container":
			container = val
		case "dir":
			dir = val
		case "host":
			host = val
		case "port":
			if pv, err := strconv.ParseUint(val, 10, 16); err != nil {
				return fmt.Errorf("port must be an unsigned integer between 1 and 65535")
			} else {
				port = uint16(pv)
			}
		case "ro":
			readOnly, err = strconv.ParseBool(val)
			if err != nil {
				return fmt.Errorf("ro must be a boolean")
			}
		default:
			return fmt.Errorf("illegal option %q", key)
		}
	}
	if container == "" {
		return fmt.Errorf("missing required option \"container\"")
	}
	if host == "" {
		host = "localhost"
	}
	if port == 0 {
		return fmt.Errorf("missing required option \"port\"")
	}
	if dir == "" {
		dir = container
	} else {
		dir = filepath.Join(container, strings.TrimPrefix(dir, "/"))
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	m, err := d.getRemoteMount(host, port, readOnly)
	if err != nil {
		return err
	}
	m.addVolume(r.Name, dir)
	return nil
}

func (d *driver) Remove(r *volume.RemoveRequest) (err error) {
	log.Debugf("Remove %s", r.Name)
	defer func() {
		logResponse(err, "Remove %s return", r.Name)
	}()
	d.lock.Lock()
	defer d.lock.Unlock()

	var v *volumeDir
	if v, err = d.getVolume(r.Name); err != nil {
		return err
	}
	if len(v.usedBy) > 0 {
		err = fmt.Errorf("volume %s is mounted by containers: %v", r.Name, v.usedBy)
	} else {
		v.deleteVolume(r.Name)
	}
	return err
}

func (d *driver) Mount(r *volume.MountRequest) (mr *volume.MountResponse, err error) {
	log.Debugf("Mount %s", r.Name)
	mr = &volume.MountResponse{}
	defer func() {
		logResponse(err, "Mount %s return %s", r.Name, mr.Mountpoint)
	}()
	d.lock.Lock()
	defer d.lock.Unlock()

	var v *volumeDir
	if v, err = d.getMountedVolume(r.Name); err != nil {
		return mr, err
	}
	if len(v.usedBy) == 0 {
		v.usedBy = []string{r.ID}
		v.createdAt = time.Now()
	} else {
		found := false
		for _, id := range v.usedBy {
			if id == r.ID {
				found = true
				break
			}
		}
		if !found {
			v.usedBy = append(v.usedBy, r.ID)
		}
	}
	mr.Mountpoint = v.logicalMountPoint()
	return mr, nil
}

func (d *driver) Path(r *volume.PathRequest) (*volume.PathResponse, error) {
	log.Debugf("Path %s", r.Name)
	d.lock.RLock()
	v, err := d.getVolume(r.Name)
	d.lock.RUnlock()
	pr := &volume.PathResponse{}
	if err == nil {
		pr.Mountpoint = v.logicalMountPoint()
	}
	logResponse(err, "Path %s return %s", r.Name, pr.Mountpoint)
	return pr, err
}

func (d *driver) Unmount(r *volume.UnmountRequest) (err error) {
	log.Debugf("Unmount %s", r.Name)
	defer func() {
		logResponse(err, "Unmount %s return", r.Name)
	}()
	d.lock.Lock()
	defer d.lock.Unlock()

	var v *volumeDir
	v, err = d.getVolume(r.Name)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	v.mounted.Store(false)
	v.usedBy = slices.DeleteFunc(v.usedBy, func(id string) bool {
		return id == r.ID
	})
	return nil
}

func (d *driver) Get(r *volume.GetRequest) (gr *volume.GetResponse, err error) {
	log.Debugf("Get %s", r.Name)
	gr = &volume.GetResponse{}
	d.lock.RLock()
	v, err := d.getVolume(r.Name)
	if err == nil {
		gr.Volume = v.asVolume(r.Name)
	}
	d.lock.RUnlock()
	logResponse(err, "Get %s return %v", r.Name, gr.Volume)
	return gr, err
}

func (d *driver) List() (*volume.ListResponse, error) {
	log.Debug("List")
	d.lock.RLock()
	var vols = make([]*volume.Volume, 0, 32)
	for _, m := range d.remoteMounts {
		vols = m.appendVolumes(vols)
	}
	d.lock.RUnlock()
	sort.Slice(vols, func(i, j int) bool {
		return vols[i].Name < vols[j].Name
	})
	log.Debugf("List return %v", vols)
	return &volume.ListResponse{Volumes: vols}, nil
}

func (d *driver) Capabilities() *volume.CapabilitiesResponse {
	return &volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "local"}}
}

func (d *driver) getRemoteMount(host string, port uint16, readOnly bool) (*remoteMount, error) {
	ps := strconv.Itoa(int(port))
	key := net.JoinHostPort(host, ps)
	if m, ok := d.remoteMounts[key]; ok && atomic.LoadInt32(&m.state) != mountDisconnected {
		if m.readOnly == readOnly {
			return m, nil
		}
		if m.readOnly {
			return nil, fmt.Errorf("writable access requested for read-only %s", key)
		}
		// Can't let a writable volume pose as read-only
		return nil, fmt.Errorf("read-only access requested writeable %s", key)
	}
	m := newRemoteMount(filepath.Join(d.volumePath, safeName(host), ps), host, port, readOnly, func(m *remoteMount) {
		// If the remote volume's state is `mountMounted` here, then it was unmounted as a consequence
		// of a broken connection to the remote host, and we must keep the entry.
		// Docker isn't aware of the broken connection and might try to mount the volume again at
		// a time when the remote host is available.
		if atomic.CompareAndSwapInt32(&m.state, mountMounted, mountDisconnected) {
			time.AfterFunc(d.disconnectedLingerTime, func() {
				d.lock.Lock()
				if m, ok := d.remoteMounts[key]; ok && atomic.LoadInt32(&m.state) == mountDisconnected && m.pingHost(d.hostPingTimeout) != nil {
					log.Debugf("Removing disconnected remote mount %s", m)
					delete(d.remoteMounts, key)
				}
				d.lock.Unlock()
			})
		} else {
			d.lock.Lock()
			delete(d.remoteMounts, key)
			d.lock.Unlock()
		}
	})
	if err := m.mountRemote(); err != nil {
		// delete it in case it was in `mountDisconnected` state.
		delete(d.remoteMounts, key)
		return nil, err
	}
	d.remoteMounts[key] = m
	return m, nil
}

// safeName returns a string that can safely be used as a file name or docker container. Only
// characters [a-zA-Z0-9][a-zA-Z0-9_.-] are allowed. Others are replaced by an underscore, or
// if it's the very first character, by the character 'a'.
func safeName(name string) string {
	n := strings.Builder{}
	for i, c := range name {
		switch {
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			n.WriteByte(byte(c))
		case i > 0 && (c == '_' || c == '.' || c == '-'):
			n.WriteByte(byte(c))
		case i > 0:
			n.WriteByte('_')
		default:
			n.WriteByte('a')
		}
	}
	return n.String()
}

func (d *driver) getVolume(n string) (v *volumeDir, err error) {
	var ok bool
	for _, m := range d.remoteMounts {
		if v, ok = m.getVolume(n); ok {
			return v, nil
		}
	}
	return nil, fmt.Errorf("no such volume: %q", n)
}

func (d *driver) getMountedVolume(n string) (v *volumeDir, err error) {
	var ok bool
	for _, m := range d.remoteMounts {
		if v, ok = m.getVolume(n); ok && atomic.LoadInt32(&m.state) != mountDisconnected {
			if err = m.perhapsMountRemote(); err != nil {
				return v, err
			}
			v.remoteMount = m
			return v, nil
		}
	}
	for key, um := range d.remoteMounts {
		if v, ok = um.getVolume(n); ok && atomic.LoadInt32(&um.state) == mountDisconnected {
			log.Debugf("Remount previously mounted remote volume %s[%s] == %s", um, n, v.logicalMountPoint())
			// Try dialing the host again with a short timeout. If it fails, then well just assume the remote is gone permanently.
			if err = um.pingHost(d.hostPingTimeout); err != nil {
				log.Debugf("Dropping previously mounted remote volume %s because the host is not responding.", um)
				delete(d.remoteMounts, key)
				break
			}
			var m *remoteMount
			m, err = d.getRemoteMount(um.host, um.port, um.readOnly)
			if err != nil {
				return nil, err
			}
			m.volumes = um.volumes
			if v, ok = m.getVolume(n); ok {
				return v, nil
			}
		}
	}
	return nil, fmt.Errorf("no such volume: %q", n)
}
