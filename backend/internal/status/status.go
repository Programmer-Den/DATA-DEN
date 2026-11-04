// Package status reports how the NAS is doing: free space on the data disk,
// the state of the RAID and how long the server and the VM have been up. The
// web interface shows it on its status page and warns when space runs low or
// a disk of the mirror is missing.
package status

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Disk levels: Low and Critical make the web interface warn.
const (
	LevelOK       = "ok"
	LevelLow      = "low"
	LevelCritical = "critical"
)

// DefaultWarnPercent is the free space, in percent of the disk, below which
// the level is Low. Below a quarter of it, it is Critical.
const DefaultWarnPercent = 10

// Config is what the status page reads.
type Config struct {
	// DataDir is the data directory; its file system is the one reported.
	DataDir string
	// WarnPercent is the free space in percent below which the disk is low;
	// 0 means DefaultWarnPercent.
	WarnPercent float64
	// MDStat is the kernel's RAID status, normally /proc/mdstat.
	MDStat string
	// Uptime is the kernel's uptime, normally /proc/uptime.
	Uptime string
	// Version and Started describe the running server.
	Version string
	Started time.Time
}

// Disk is the space on the data disk.
type Disk struct {
	Total int64 `json:"total"`
	Free  int64 `json:"free"`
	Used  int64 `json:"used"`
	// FreePercent is Free in percent of Total.
	FreePercent float64 `json:"freePercent"`
	Level       string  `json:"level"`
	// WarnPercent is the threshold behind Level, for the page to explain it.
	WarnPercent float64 `json:"warnPercent"`
}

// Report is the answer of GET /api/status.
type Report struct {
	Version string    `json:"version"`
	Started time.Time `json:"started"`
	// Booted is when the VM started, zero when unknown.
	Booted time.Time `json:"booted,omitzero"`
	// Disk is nil when the data directory cannot be read; DiskError says why.
	Disk      *Disk  `json:"disk"`
	DiskError string `json:"diskError,omitempty"`
	// RAID is nil when the kernel reports no RAID, such as outside the VM.
	RAID []Array `json:"raid"`
}

// Status serves GET /api/status.
type Status struct {
	cfg Config
	now func() time.Time
}

// New returns the status page's API for cfg.
func New(cfg Config) *Status {
	if cfg.WarnPercent <= 0 {
		cfg.WarnPercent = DefaultWarnPercent
	}
	return &Status{cfg: cfg, now: time.Now}
}

// WarnPercent reads DATA_DEN_DISK_WARN_PERCENT's value s: empty means the
// default, anything else must be a number above 0 and below 100.
func WarnPercent(s string) (float64, error) {
	if s == "" {
		return DefaultWarnPercent, nil
	}
	p, err := strconv.ParseFloat(s, 64)
	if err != nil || p <= 0 || p >= 100 {
		return 0, &invalidPercentError{s}
	}
	return p, nil
}

type invalidPercentError struct{ value string }

func (e *invalidPercentError) Error() string {
	return "DATA_DEN_DISK_WARN_PERCENT must be a number above 0 and below 100, not " + strconv.Quote(e.value)
}

// Register adds GET /api/status to mux, behind require (the login check).
func (s *Status) Register(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/status", require(s.serve))
}

func (s *Status) serve(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(s.Report())
}

// Report gathers the current status.
func (s *Status) Report() Report {
	r := Report{Version: s.cfg.Version, Started: s.cfg.Started}
	if disk, err := s.disk(); err != nil {
		slog.Warn("reading the free space of the data directory", "dir", s.cfg.DataDir, "err", err)
		r.DiskError = "Speicherplatz nicht lesbar"
	} else {
		r.Disk = disk
	}
	if s.cfg.MDStat != "" {
		if data, err := os.ReadFile(s.cfg.MDStat); err == nil {
			r.RAID = ParseMDStat(string(data))
		}
	}
	if s.cfg.Uptime != "" {
		if data, err := os.ReadFile(s.cfg.Uptime); err == nil {
			r.Booted = booted(string(data), s.now())
		}
	}
	return r
}

func (s *Status) disk() (*Disk, error) {
	total, free, err := space(s.cfg.DataDir)
	if err != nil {
		return nil, err
	}
	return newDisk(total, free, s.cfg.WarnPercent), nil
}

func newDisk(total, free int64, warnPercent float64) *Disk {
	d := &Disk{Total: total, Free: free, Used: total - free, Level: LevelOK, WarnPercent: warnPercent}
	if total > 0 {
		d.FreePercent = float64(free) * 100 / float64(total)
	}
	switch {
	case d.FreePercent < warnPercent/4:
		d.Level = LevelCritical
	case d.FreePercent < warnPercent:
		d.Level = LevelLow
	}
	return d
}

// booted is the boot time from /proc/uptime ("12345.67 98765.43"), zero when
// the content is not understood.
func booted(uptime string, now time.Time) time.Time {
	first, _, _ := strings.Cut(strings.TrimSpace(uptime), " ")
	seconds, err := strconv.ParseFloat(first, 64)
	if err != nil || seconds < 0 {
		return time.Time{}
	}
	return now.Add(-time.Duration(seconds * float64(time.Second))).Truncate(time.Second)
}
