package sftp

import (
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/go-plugins-helpers/volume"

	"github.com/datawire/docker-volume-telemount/pkg/log"
)

type driver struct {
	sync.Mutex
	volumePath   string
	remoteMounts map[string]*mount
}

func NewDriver() volume.Driver {
	log.Debug("NewDriver")
	volumePath := filepath.Join("/mnt", "volumes")
	d := &driver{
		volumePath:   volumePath,
		remoteMounts: make(map[string]*mount),
	}
	return d
}

func (d *driver) Create(r *volume.CreateRequest) error {
	log.Debugf("Create %s", r.Name)
	d.Lock()
	defer d.Unlock()

	var container, dir string
	var port uint16
	var hostIP net.IP
	for key, val := range r.Options {
		switch key {
		case "container":
			container = val
		case "dir":
			dir = val
		case "port":
			if pv, err := strconv.ParseUint(val, 10, 16); err != nil {
				return log.Errorf("port must be an unsigned integer between 1 and 65535")
			} else {
				port = uint16(pv)
			}
		case "ip":
			if ip := net.ParseIP(val); ip != nil {
				if ip4 := ip.To4(); ip4 != nil {
					hostIP = ip4
				} else if ip16 := ip.To16(); ip16 != nil {
					hostIP = ip16
				}
			}
			if hostIP == nil {
				return log.Errorf("invalid IP %q", val)
			}
		default:
			return log.Errorf("illegal option %q", key)
		}
	}
	if container == "" {
		return log.Errorf("missing required option \"container\"")
	}
	if port == 0 {
		return log.Errorf("missing required option \"port\"")
	}
	if hostIP == nil {
		return log.Errorf("missing required option \"ip\"")
	}
	if dir == "" {
		dir = container
	} else {
		dir = filepath.Join(container, strings.TrimPrefix(dir, "/"))
	}
	d.getRemoteMount(hostIP, port).addVolume(r.Name, dir)
	return nil
}

func (d *driver) Remove(r *volume.RemoveRequest) error {
	log.Debugf("Remove %s", r.Name)
	d.Lock()
	defer d.Unlock()

	v, err := d.getVolume(r.Name, false)
	if err != nil {
		return err
	}
	if len(v.usedBy) > 0 {
		return log.Errorf("volume %s is mounted by containers: %v", r.Name, v.usedBy)
	}
	return v.deleteVolume(r.Name)
}

func (d *driver) Mount(r *volume.MountRequest) (*volume.MountResponse, error) {
	log.Debugf("Mount %s", r.Name)
	d.Lock()
	defer d.Unlock()

	v, err := d.getVolume(r.Name, true)
	if err != nil {
		return nil, err
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
	return &volume.MountResponse{Mountpoint: v.logicalMountPoint()}, nil
}

func (d *driver) Path(r *volume.PathRequest) (*volume.PathResponse, error) {
	log.Debugf("Path %s", r.Name)
	d.Lock()
	v, err := d.getVolume(r.Name, false)
	d.Unlock()
	if err != nil {
		return nil, err
	}
	return &volume.PathResponse{Mountpoint: v.logicalMountPoint()}, nil
}

func (d *driver) Unmount(r *volume.UnmountRequest) error {
	log.Debugf("Unmount %s", r.Name)
	d.Lock()
	defer d.Unlock()

	v, err := d.getVolume(r.Name, false)
	if err != nil {
		return err
	}
	found := false
	for i, id := range v.usedBy {
		if id == r.ID {
			found = true
			last := len(v.usedBy) - 1
			if last > 0 {
				v.usedBy[i] = v.usedBy[last]
				v.usedBy = v.usedBy[:last]
			} else {
				v.usedBy = nil
			}
			break
		}
	}
	if !found {
		return log.Errorf("container %s has no mount for volume %s", r.ID, r.Name)
	}
	if len(v.usedBy) == 0 {
		return v.mount.perhapsUnmount()
	}
	return nil
}

func (d *driver) Get(r *volume.GetRequest) (*volume.GetResponse, error) {
	log.Debugf("Get %s", r.Name)
	d.Lock()
	v, err := d.getVolume(r.Name, false)
	d.Unlock()
	if err != nil {
		return nil, err
	}
	return &volume.GetResponse{Volume: v.asVolume(r.Name)}, nil
}

func (d *driver) List() (*volume.ListResponse, error) {
	log.Debug("List")
	d.Lock()
	var vols = make([]*volume.Volume, 0, 32)
	for _, m := range d.remoteMounts {
		vols = m.appendVolumes(vols)
	}
	d.Unlock()
	sort.Slice(vols, func(i, j int) bool {
		return vols[i].Name < vols[j].Name
	})
	return &volume.ListResponse{Volumes: vols}, nil
}

func (d *driver) Capabilities() *volume.CapabilitiesResponse {
	return &volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "local"}}
}

func makeAddrKey(ip net.IP, port uint16) string {
	il := len(ip)
	key := make([]byte, il+2)
	copy(key, ip)
	key[il] = byte(port >> 8)
	key[il+1] = byte(port)
	return string(key)
}

func (d *driver) getRemoteMount(ip net.IP, port uint16) *mount {
	key := makeAddrKey(ip, port)
	if m, ok := d.remoteMounts[key]; ok {
		return m
	}
	m := newMount(filepath.Join(d.volumePath, ip.String(), strconv.Itoa(int(port))), ip, port)
	d.remoteMounts[key] = m
	return m
}

func (d *driver) getVolume(n string, mounted bool) (*volumeDir, error) {
	for _, m := range d.remoteMounts {
		if v, ok := m.getVolume(n); ok {
			_ = m.assertMounted(mounted)
			return v, nil
		}
	}
	return nil, log.Errorf("no such volume: %q", n)
}
