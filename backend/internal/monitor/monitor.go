// Package monitor lets a den-watcher hub on the Pi watch the NAS: it answers
// GET /api/metrics as a den-watcher device does, on a port of its own, with
// the VM's usage, the free space of the mirror and the state of the RAID, the
// disks' health from Windows, the backup and the last deploy as extras.
//
// A hub sends the pairing code it was given in every request; without the
// right one the answer is 403, with the header the hub reads to ask for it.
// den-watcher reads devices over plain HTTP on the local network, so the
// port is only for the Pi (vm/nftables.conf), and it tells nothing but the
// state of the NAS: no file names, no accounts.
package monitor

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/status"
)

// den-watcher's pairing: the header with the code, the header that tells a
// hub why it was refused, and the characters of a code.
const (
	CodeHeader    = "Usage-Control-Pairing-Code"
	ProblemHeader = "Usage-Control-Pairing"
	alphabet      = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	codeLength    = 8
)

// wrongDelay is how long an answer to a wrong code waits, so codes cannot be
// tried quickly.
const wrongDelay = time.Second

// Config is what the monitor reports.
type Config struct {
	// Name is the name a hub offers for the device.
	Name string
	// Version is DATA-DEN's version.
	Version string
	// Code is the pairing code a hub must send, as Code checks it.
	Code string
	// Status reads the free space and the RAID.
	Status *status.Status
	// DataReady tells whether the data directory could be opened; nil
	// leaves it out.
	DataReady func() bool
	// StateDir is the VM's /var/lib/data-den, where its scripts leave the
	// disk health, the backup's and the last deploy's result; empty leaves
	// them out.
	StateDir string
	// Proc is the kernel's /proc, normally "/proc"; a container shares the
	// VM's for the memory, the CPU and the uptime.
	Proc string
}

// Monitor answers a den-watcher hub.
type Monitor struct {
	cfg  Config
	code string
	now  func() time.Time
	wait func(time.Duration)

	// mu guards the CPU times of the previous reading, which the usage is
	// measured against.
	mu   sync.Mutex
	prev cpuTimes
}

// New returns the monitor for cfg.
func New(cfg Config) *Monitor {
	if cfg.Proc == "" {
		cfg.Proc = "/proc"
	}
	return &Monitor{cfg: cfg, code: normalize(cfg.Code), now: time.Now, wait: time.Sleep}
}

// Code checks DATA_DEN_MONITOR_CODE's value s: a den-watcher pairing code,
// 8 digits and capital letters without 0, 1, I and O, written as two groups
// of four such as 7KQM-X2RP.
func Code(s string) (string, error) {
	c := normalize(s)
	if len(c) != codeLength || strings.Trim(c, alphabet) != "" {
		return "", errors.New("DATA_DEN_MONITOR_CODE must be a pairing code for den-watcher: 8 digits and capital letters without 0, 1, I and O, such as " + NewCode())
	}
	return c[:4] + "-" + c[4:], nil
}

// NewCode returns a random pairing code, for the error that asks for one.
func NewCode() string {
	b := make([]byte, codeLength)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

// normalize returns code as it is compared: in capitals, without the spaces
// and dashes it may be written with, as den-watcher compares it.
func normalize(code string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(code)))
}

// Handler serves GET /api/metrics. Everything else, /api/minutes too, is
// 404, which tells a hub to keep averages of its own.
func (m *Monitor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/metrics", m.serve)
	return mux
}

func (m *Monitor) serve(w http.ResponseWriter, r *http.Request) {
	sent := r.Header.Get(CodeHeader)
	switch {
	case sent == "":
		w.Header().Set(ProblemHeader, "needed")
		http.Error(w, "a pairing code is needed", http.StatusForbidden)
		return
	case subtle.ConstantTimeCompare([]byte(normalize(sent)), []byte(m.code)) != 1:
		m.wait(wrongDelay)
		w.Header().Set(ProblemHeader, "wrong")
		http.Error(w, "wrong pairing code", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(m.Snapshot())
}
