package incus

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"

	"myringa/internal/format"
)

// InstanceRow holds formatted data for a single instance.
type InstanceRow struct {
	Name, Type, Status, CPU, Memory, Disk, IPv4 string
	CPULimit    string
	MemoryLimit string
}

// SnapshotInfo holds summary data for an instance snapshot.
type SnapshotInfo struct {
	Name      string
	CreatedAt time.Time
	Stateful  bool
	Size      int64
}

// ImageInfo holds summary data for an image alias.
type ImageInfo struct {
	Alias       string
	Description string
}

// ProfileInfo holds summary data for a profile.
type ProfileInfo struct {
	Name        string
	Description string
}

// CPUSnapshot stores the last known CPU usage for delta calculation.
type CPUSnapshot struct {
	UsageNS   int64
	Timestamp time.Time
}

// Client is the interface for all Incus operations the UI needs.
type Client interface {
	FetchInstances(ctx context.Context, prev map[string]CPUSnapshot) ([]InstanceRow, map[string]CPUSnapshot, error)
	StartInstance(ctx context.Context, name string) error
	StopInstance(ctx context.Context, name string) error
	RestartInstance(ctx context.Context, name string) error
	DeleteInstance(ctx context.Context, name string) error
	CreateSnapshot(ctx context.Context, instanceName, snapshotName string) error
	RestoreSnapshot(ctx context.Context, instanceName, snapshotName string) error
	DeleteSnapshot(ctx context.Context, instanceName, snapshotName string) error
	ListSnapshots(ctx context.Context, instanceName string) ([]SnapshotInfo, error)
	ListImages(ctx context.Context) ([]ImageInfo, error)
	ListProfiles(ctx context.Context) ([]ProfileInfo, error)
	CreateInstance(ctx context.Context, name, imageAlias, profile string) error
}

// client implements Client using the Incus SDK over a local Unix socket.
type client struct {
	conn incusclient.InstanceServer
}

// Connect returns a Client connected to the local Incus daemon.
func Connect() (Client, error) {
	conn, err := incusclient.ConnectIncusUnix("", nil)
	if err != nil {
		return nil, err
	}
	return &client{conn: conn}, nil
}

// FetchInstances retrieves all instances and their state in one call,
// computes CPU deltas from prev snapshots, and returns formatted rows
// sorted by name.
func (c *client) FetchInstances(_ context.Context, prev map[string]CPUSnapshot) ([]InstanceRow, map[string]CPUSnapshot, error) {
	instances, err := c.conn.GetInstancesFull(api.InstanceTypeAny)
	if err != nil {
		return nil, nil, err
	}

	rows := make([]InstanceRow, 0, len(instances))
	snaps := make(map[string]CPUSnapshot, len(instances))
	now := time.Now()

	for _, inst := range instances {
		row := InstanceRow{
			Name:   inst.Name,
			Type:   instanceType(inst.Type),
			Status: inst.Status,
			CPU:    "—",
			Memory: "—",
			Disk:   "—",
			IPv4:   "—",
		}

		if v := inst.Config["limits.cpu"]; v != "" {
			row.CPULimit = v
		}
		if v := inst.Config["limits.memory"]; v != "" {
			row.MemoryLimit = v
		}

		if inst.State != nil {
			// CPU — delta from previous snapshot
			curNS := inst.State.CPU.Usage
			snaps[inst.Name] = CPUSnapshot{UsageNS: curNS, Timestamp: now}

			if p, ok := prev[inst.Name]; ok && inst.Status == "Running" {
				deltaNS := curNS - p.UsageNS
				elapsed := now.Sub(p.Timestamp).Seconds()
				if deltaNS >= 0 && elapsed > 0 {
					pct := float64(deltaNS) / (elapsed * 1e9) * 100
					row.CPU = fmt.Sprintf("%.1f%%", pct)
				}
			}

			// Memory
			if inst.State.Memory.Usage > 0 || inst.State.Memory.Total > 0 {
				row.Memory = format.Memory(inst.State.Memory.Usage, inst.State.Memory.Total)
			}

			// Disk
			row.Disk = rootDiskUsage(inst.State.Disk)

			// IPv4
			row.IPv4 = allIPv4(inst.State.Network)
		}

		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Name < rows[j].Name
	})

	return rows, snaps, nil
}

func (c *client) StartInstance(ctx context.Context, name string) error {
	op, err := c.conn.UpdateInstanceState(name, api.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}, "")
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) StopInstance(ctx context.Context, name string) error {
	op, err := c.conn.UpdateInstanceState(name, api.InstanceStatePut{
		Action:  "stop",
		Timeout: 30,
	}, "")
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) RestartInstance(ctx context.Context, name string) error {
	op, err := c.conn.UpdateInstanceState(name, api.InstanceStatePut{
		Action:  "restart",
		Timeout: 30,
	}, "")
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) DeleteInstance(ctx context.Context, name string) error {
	op, err := c.conn.DeleteInstance(name)
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) ListImages(_ context.Context) ([]ImageInfo, error) {
	aliases, err := c.conn.GetImageAliases()
	if err != nil {
		return nil, err
	}
	images := make([]ImageInfo, 0, len(aliases))
	for _, a := range aliases {
		images = append(images, ImageInfo{
			Alias:       a.Name,
			Description: a.Description,
		})
	}
	sort.Slice(images, func(i, j int) bool {
		return images[i].Alias < images[j].Alias
	})
	return images, nil
}

func (c *client) ListProfiles(_ context.Context) ([]ProfileInfo, error) {
	profiles, err := c.conn.GetProfiles()
	if err != nil {
		return nil, err
	}
	result := make([]ProfileInfo, 0, len(profiles))
	for _, p := range profiles {
		result = append(result, ProfileInfo{
			Name:        p.Name,
			Description: p.Description,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (c *client) CreateInstance(ctx context.Context, name, imageAlias, profile string) error {
	req := api.InstancesPost{
		Name: name,
		Source: api.InstanceSource{
			Type:  "image",
			Alias: imageAlias,
		},
		InstancePut: api.InstancePut{
			Profiles: []string{profile},
		},
	}
	op, err := c.conn.CreateInstance(req)
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) CreateSnapshot(ctx context.Context, instanceName, snapshotName string) error {
	op, err := c.conn.CreateInstanceSnapshot(instanceName, api.InstanceSnapshotsPost{
		Name: snapshotName,
	})
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) RestoreSnapshot(ctx context.Context, instanceName, snapshotName string) error {
	op, err := c.conn.UpdateInstance(instanceName, api.InstancePut{
		Restore: snapshotName,
	}, "")
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) DeleteSnapshot(ctx context.Context, instanceName, snapshotName string) error {
	op, err := c.conn.DeleteInstanceSnapshot(instanceName, snapshotName)
	if err != nil {
		return err
	}
	return op.WaitContext(ctx)
}

func (c *client) ListSnapshots(_ context.Context, instanceName string) ([]SnapshotInfo, error) {
	snapshots, err := c.conn.GetInstanceSnapshots(instanceName)
	if err != nil {
		return nil, err
	}
	result := make([]SnapshotInfo, 0, len(snapshots))
	for _, s := range snapshots {
		result = append(result, SnapshotInfo{
			Name:      s.Name,
			CreatedAt: s.CreatedAt,
			Stateful:  s.Stateful,
			Size:      s.Size,
		})
	}
	return result, nil
}

// Unexported helpers

func allIPv4(networks map[string]api.InstanceStateNetwork) string {
	names := make([]string, 0, len(networks))
	for name := range networks {
		if name == "lo" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var ips []string
	for _, name := range names {
		for _, addr := range networks[name].Addresses {
			if addr.Family == "inet" && addr.Scope == "global" {
				ips = append(ips, addr.Address)
			}
		}
	}
	if len(ips) == 0 {
		return "—"
	}
	return strings.Join(ips, ", ")
}

func rootDiskUsage(disks map[string]api.InstanceStateDisk) string {
	if d, ok := disks["root"]; ok && d.Usage > 0 {
		return format.Bytes(d.Usage)
	}
	var total int64
	for _, d := range disks {
		total += d.Usage
	}
	if total > 0 {
		return format.Bytes(total)
	}
	return "—"
}

func instanceType(t string) string {
	switch t {
	case "container":
		return "CT"
	case "virtual-machine":
		return "VM"
	default:
		return t
	}
}
