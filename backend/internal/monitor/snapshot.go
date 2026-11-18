package monitor

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Snapshot is what GET /api/metrics answers, in the fields of den-watcher's
// snapshot (its docs/data.md, "The snapshot"). What the VM does not have,
// such as temperatures or GPUs, stays an empty list, as a device sends it.
type Snapshot struct {
	Name          string     `json:"name,omitempty"`
	Time          time.Time  `json:"time"`
	UptimeSeconds uint64     `json:"uptimeSeconds"`
	CPU           CPU        `json:"cpu"`
	Memory        Memory     `json:"memory"`
	Temperatures  []struct{} `json:"temperatures"`
	Disks         []Disk     `json:"disks"`
	Network       []struct{} `json:"network"`
	GPUs          []struct{} `json:"gpus"`
	Extras        []Extra    `json:"extras,omitempty"`
}

// CPU is the VM's processor: its usage since the previous reading, which a
// hub asks for every 5 seconds.
type CPU struct {
	UsagePercent  float64      `json:"usagePercent"`
	Cores         int          `json:"cores"`
	IOWaitPercent *float64     `json:"ioWaitPercent,omitempty"`
	LoadAverage   *LoadAverage `json:"loadAverage,omitempty"`
}

// LoadAverage is the load over 1, 5 and 15 minutes.
type LoadAverage struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

// Memory is the VM's memory.
type Memory struct {
	TotalBytes     uint64  `json:"totalBytes"`
	UsedBytes      uint64  `json:"usedBytes"`
	UsedPercent    float64 `json:"usedPercent"`
	AvailableBytes uint64  `json:"availableBytes,omitempty"`
	CachedBytes    uint64  `json:"cachedBytes,omitempty"`
	Swap           *Swap   `json:"swap,omitempty"`
}

// Swap is the VM's swap space.
type Swap struct {
	TotalBytes  uint64  `json:"totalBytes"`
	UsedBytes   uint64  `json:"usedBytes"`
	UsedPercent float64 `json:"usedPercent"`
}

// Disk is the space on the mirror; den-watcher warns from 90 % used and
// alerts from 95 %.
type Disk struct {
	Path        string  `json:"path"`
	TotalBytes  uint64  `json:"totalBytes"`
	UsedBytes   uint64  `json:"usedBytes"`
	UsedPercent float64 `json:"usedPercent"`
}

// cpuTimes are the jiffies of the first line of /proc/stat.
type cpuTimes struct {
	total, idle, iowait uint64
}

// Snapshot reads the NAS now.
func (m *Monitor) Snapshot() Snapshot {
	report := m.cfg.Status.Report()
	s := Snapshot{
		Name:         m.cfg.Name,
		Time:         m.now().UTC(),
		Temperatures: []struct{}{},
		Disks:        []Disk{},
		Network:      []struct{}{},
		GPUs:         []struct{}{},
	}
	s.UptimeSeconds = m.uptime()
	s.CPU = m.cpu()
	s.Memory = m.memory()
	if d := report.Disk; d != nil && d.Total > 0 {
		s.Disks = append(s.Disks, Disk{
			Path:        diskPath,
			TotalBytes:  uint64(d.Total),
			UsedBytes:   uint64(max(d.Used, 0)),
			UsedPercent: round(float64(d.Used) * 100 / float64(d.Total)),
		})
	}
	s.Extras = m.extras(report)
	return s
}

// diskPath is what the mirror is called on the hub's page: where it is
// mounted in the VM.
const diskPath = "/srv/data-den"

func (m *Monitor) read(name string) string {
	data, err := os.ReadFile(filepath.Join(m.cfg.Proc, name)) //nolint:gosec // a file of /proc, by a fixed name
	if err != nil {
		return ""
	}
	return string(data)
}

// uptime is the VM's uptime in seconds, 0 when unknown.
func (m *Monitor) uptime() uint64 {
	first, _, _ := strings.Cut(strings.TrimSpace(m.read("uptime")), " ")
	seconds, err := strconv.ParseFloat(first, 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return uint64(seconds)
}

// cpu reads /proc/stat and /proc/loadavg. The usage is measured against the
// previous reading, so the first one reports 0.
func (m *Monitor) cpu() CPU {
	var c CPU
	var now cpuTimes
	for line := range strings.SplitSeq(m.read("stat"), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			c.Cores++
			continue
		}
		// user nice system idle iowait irq softirq steal: guest time is
		// already in user and nice.
		for i, f := range fields[1:min(len(fields), 9)] {
			v, _ := strconv.ParseUint(f, 10, 64)
			now.total += v
			switch i {
			case 3:
				now.idle += v
			case 4:
				now.idle += v
				now.iowait = v
			}
		}
	}
	m.mu.Lock()
	prev := m.prev
	m.prev = now
	m.mu.Unlock()
	if prev.total > 0 && now.total > prev.total && now.idle >= prev.idle && now.iowait >= prev.iowait {
		total := float64(now.total - prev.total)
		c.UsagePercent = round(100 - float64(now.idle-prev.idle)*100/total)
		iowait := round(float64(now.iowait-prev.iowait) * 100 / total)
		c.IOWaitPercent = &iowait
	}
	if f := strings.Fields(m.read("loadavg")); len(f) >= 3 {
		one, err1 := strconv.ParseFloat(f[0], 64)
		five, err2 := strconv.ParseFloat(f[1], 64)
		fifteen, err3 := strconv.ParseFloat(f[2], 64)
		if err1 == nil && err2 == nil && err3 == nil {
			c.LoadAverage = &LoadAverage{One: one, Five: five, Fifteen: fifteen}
		}
	}
	return c
}

// memory reads /proc/meminfo.
func (m *Monitor) memory() Memory {
	kb := map[string]uint64{}
	for line := range strings.SplitSeq(m.read("meminfo"), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if f := strings.Fields(rest); len(f) > 0 {
			if v, err := strconv.ParseUint(f[0], 10, 64); err == nil {
				kb[name] = v * 1024
			}
		}
	}
	var mem Memory
	mem.TotalBytes = kb["MemTotal"]
	if mem.TotalBytes == 0 {
		return mem
	}
	mem.AvailableBytes = min(kb["MemAvailable"], mem.TotalBytes)
	mem.CachedBytes = kb["Cached"]
	mem.UsedBytes = mem.TotalBytes - mem.AvailableBytes
	mem.UsedPercent = round(float64(mem.UsedBytes) * 100 / float64(mem.TotalBytes))
	if total := kb["SwapTotal"]; total > 0 {
		used := total - min(kb["SwapFree"], total)
		mem.Swap = &Swap{TotalBytes: total, UsedBytes: used, UsedPercent: round(float64(used) * 100 / float64(total))}
	}
	return mem
}

// round keeps two decimals.
func round(v float64) float64 {
	return math.Round(v*100) / 100
}
