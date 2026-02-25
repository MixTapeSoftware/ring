package incus

import (
	"fmt"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
)

// InstanceRow holds formatted data for a single instance.
type InstanceRow struct {
	Name, Type, Status, CPU, Memory, Disk, IPv4 string
}

// CPUSnapshot stores the last known CPU usage for delta calculation.
type CPUSnapshot struct {
	UsageNS   int64
	Timestamp time.Time
}

// Connect returns an Incus client connected via the local Unix socket.
func Connect() (incus.InstanceServer, error) {
	return incus.ConnectIncusUnix("", nil)
}

// FetchInstances retrieves all instances and their state in one call,
// computes CPU deltas from prev snapshots, and returns formatted rows.
func FetchInstances(c incus.InstanceServer, prev map[string]CPUSnapshot) ([]InstanceRow, map[string]CPUSnapshot, error) {
	instances, err := c.GetInstancesFull(api.InstanceTypeAny)
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

		if inst.State != nil {
			// CPU
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
				row.Memory = formatMemory(inst.State.Memory.Usage, inst.State.Memory.Total)
			}

			// Disk
			row.Disk = rootDiskUsage(inst.State.Disk)

			// IPv4
			row.IPv4 = allIPv4(inst.State.Network)
		}

		rows = append(rows, row)
	}

	return rows, snaps, nil
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.0fG", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

func formatMemory(used, total int64) string {
	if total > 0 {
		return fmt.Sprintf("%s/%s", formatBytes(used), formatBytes(total))
	}
	return formatBytes(used)
}

func allIPv4(networks map[string]api.InstanceStateNetwork) string {
	// Sort interface names for stable output
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
		return formatBytes(d.Usage)
	}
	// Sum all disk usage as fallback
	var total int64
	for _, d := range disks {
		total += d.Usage
	}
	if total > 0 {
		return formatBytes(total)
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
