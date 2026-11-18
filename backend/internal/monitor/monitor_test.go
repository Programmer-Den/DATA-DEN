package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/status"
)

const healthyRAID = `Personalities : [raid1]
md127 : active raid1 sdc[1] sdb[0]
      3906886464 blocks super 1.2 [2/2] [UU]

unused devices: <none>
`

const degradedRAID = `Personalities : [raid1]
md127 : active raid1 sdc[1](F) sdb[0]
      3906886464 blocks super 1.2 [2/1] [U_]

unused devices: <none>
`

var now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// setup returns a monitor over a fake /proc, mdstat and state directory.
func setup(t *testing.T, mdstat string, state map[string]string) *Monitor {
	t.Helper()
	dir := t.TempDir()
	proc := filepath.Join(dir, "proc")
	stateDir := filepath.Join(dir, "state")
	data := filepath.Join(dir, "data")
	for _, d := range []string{proc, stateDir, data} {
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(proc, "uptime"), "3600.50 7000.00\n")
	write(filepath.Join(proc, "loadavg"), "0.50 0.25 0.10 1/200 1234\n")
	write(filepath.Join(proc, "stat"), "cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 50 0 50 350 50 0 0 0 0 0\ncpu1 50 0 50 350 50 0 0 0 0 0\nintr 1\n")
	write(filepath.Join(proc, "meminfo"), "MemTotal: 4000 kB\nMemFree: 1000 kB\nMemAvailable: 3000 kB\nCached: 1500 kB\nSwapTotal: 1000 kB\nSwapFree: 750 kB\n")
	write(filepath.Join(dir, "mdstat"), mdstat)
	for name, content := range state {
		write(filepath.Join(stateDir, name), content)
	}
	m := New(Config{
		Name:      "DATA-DEN",
		Version:   "1.2.3",
		Code:      "7KQM-X2RP",
		Status:    status.New(status.Config{DataDir: data, MDStat: filepath.Join(dir, "mdstat")}),
		DataReady: func() bool { return true },
		StateDir:  stateDir,
		Proc:      proc,
	})
	m.now = func() time.Time { return now }
	return m
}

func TestCode(t *testing.T) {
	for _, ok := range []string{"7KQM-X2RP", "7kqmx2rp", " 7KQM X2RP "} {
		if got, err := Code(ok); err != nil || got != "7KQM-X2RP" {
			t.Errorf("Code(%q) = %q, %v", ok, got, err)
		}
	}
	for _, bad := range []string{"", "7KQM-X2R", "7KQM-X2RP1", "7KQM-X2R0", "IOIO-IOIO"} {
		if _, err := Code(bad); err == nil || !strings.Contains(err.Error(), "DATA_DEN_MONITOR_CODE") {
			t.Errorf("Code(%q) = %v, want an error", bad, err)
		}
	}
	if _, err := Code(NewCode()); err != nil {
		t.Errorf("NewCode makes a code Code refuses: %v", err)
	}
}

func TestPairing(t *testing.T) {
	m := setup(t, healthyRAID, nil)
	var waited time.Duration
	m.wait = func(d time.Duration) { waited += d }
	tests := []struct {
		name, code, problem string
		want                int
	}{
		{"no code", "", "needed", http.StatusForbidden},
		{"wrong code", "AAAA-AAAA", "wrong", http.StatusForbidden},
		{"right code", "7kqm-x2rp", "", http.StatusOK},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
		if tt.code != "" {
			r.Header.Set(CodeHeader, tt.code)
		}
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, r)
		if w.Code != tt.want || w.Header().Get(ProblemHeader) != tt.problem {
			t.Errorf("%s: %d %q, want %d %q", tt.name, w.Code, w.Header().Get(ProblemHeader), tt.want, tt.problem)
		}
	}
	if waited != wrongDelay {
		t.Errorf("waited %v, want %v for the one wrong code", waited, wrongDelay)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/minutes?after=0", nil)
	r.Header.Set(CodeHeader, "7KQM-X2RP")
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("/api/minutes: %d, want 404 so a hub keeps averages of its own", w.Code)
	}
}

func TestSnapshot(t *testing.T) {
	m := setup(t, healthyRAID, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	r.Header.Set(CodeHeader, "7KQM-X2RP")
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, r)
	var s map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	// The lists a den-watcher page expects, even when empty.
	for _, list := range []string{"temperatures", "disks", "network", "gpus"} {
		if _, ok := s[list].([]any); !ok {
			t.Errorf("%s is %v, want a list", list, s[list])
		}
	}
	snap := m.Snapshot()
	if snap.UptimeSeconds != 3600 || snap.CPU.Cores != 2 || snap.CPU.LoadAverage == nil || snap.CPU.LoadAverage.Five != 0.25 {
		t.Errorf("uptime %d, cpu %+v", snap.UptimeSeconds, snap.CPU)
	}
	if mem := snap.Memory; mem.TotalBytes != 4000*1024 || mem.UsedBytes != 1000*1024 || mem.UsedPercent != 25 || mem.Swap == nil || mem.Swap.UsedPercent != 25 {
		t.Errorf("memory %+v", mem)
	}
	if len(snap.Disks) != 1 || snap.Disks[0].Path != diskPath || snap.Disks[0].TotalBytes == 0 {
		t.Errorf("disks %+v", snap.Disks)
	}
}

func TestCPUUsage(t *testing.T) {
	m := setup(t, healthyRAID, nil)
	if c := m.cpu(); c.UsagePercent != 0 || c.IOWaitPercent != nil {
		t.Errorf("first reading %+v, want no usage yet", c)
	}
	// 1000 jiffies later: 600 idle, 100 waiting, 300 busy.
	stat := "cpu  250 0 250 1300 200 0 0 0 0 0\ncpu0 1 1 1 1\ncpu1 1 1 1 1\n"
	if err := os.WriteFile(filepath.Join(m.cfg.Proc, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := m.cpu(); c.UsagePercent != 30 || c.IOWaitPercent == nil || *c.IOWaitPercent != 10 {
		t.Errorf("usage %+v, want 30 %% and 10 %% waiting", c)
	}
}

// items returns the values of the snapshot's extras by group and id.
func items(m *Monitor) map[string]Item {
	all := map[string]Item{}
	for _, g := range m.Snapshot().Extras {
		for _, it := range g.Items {
			all[g.ID+"/"+it.ID] = it
		}
	}
	return all
}

func TestExtras(t *testing.T) {
	recent := now.Add(-time.Hour).Format(time.RFC3339)
	old := now.Add(-72 * time.Hour).Format(time.RFC3339)
	tests := []struct {
		name   string
		mdstat string
		state  map[string]string
		// want maps group/id to the text and alert it must have.
		want map[string][2]string
		// absent are values there must not be.
		absent []string
	}{
		{
			name:   "nothing set up in the VM",
			mdstat: healthyRAID,
			want: map[string][2]string{
				"smart-checks/raid-md127-health": {passed, ""},
				"data-den/files":                 {"ready", ""},
				"data-den/version":               {"1.2.3", ""},
			},
			absent: []string{"data-den/backup", "data-den/deploy", "data-den/disk-check", "smart-checks/disks-health"},
		},
		{
			name:   "all fine",
			mdstat: healthyRAID,
			state: map[string]string{
				diskHealthFile: "status=ok\nchecked=" + recent + "\n",
				backupFile:     "result=success\nfinished=" + recent + "\nsuccess=" + recent + "\n",
				deployFile:     "id=abc\nstatus=ok\ntag=1.2.3\nDATA-DEN runs '1.2.3' and is healthy.\n",
			},
			want: map[string][2]string{
				"smart-checks/disks-health": {passed, ""},
				"data-den/disk-check":       {"all disks fine, 2026-11-18 11:00 UTC", ""},
				"data-den/backup":           {"fine, 2026-11-18 11:00 UTC", ""},
				"data-den/deploy":           {"1.2.3, ", ""},
			},
		},
		{
			name:   "a disk gone, Windows finds problems, the backup failed",
			mdstat: degradedRAID,
			state: map[string]string{
				diskHealthFile: "status=warn\nchecked=" + recent + "\nPlatte 2 (WDC, X1): Zustand laut SMART: Warning; 3 nicht korrigierbare Lesefehler\n",
				backupFile:     "result=exit-code\nfinished=" + recent + "\nsuccess=" + old + "\n",
				deployFile:     "id=abc\nstatus=failed\ntag=1.2.4\nPulling failed\n",
			},
			want: map[string][2]string{
				"smart-checks/raid-md127-health":           {failed, Critical},
				"smart-checks/disk-platte-2-wdc-x1-health": {failed, Critical},
				"data-den/disk-check":                      {"problems, 2026-11-18 11:00 UTC", Critical},
				"data-den/backup":                          {"FAILED 2026-11-18 11:00 UTC (exit-code)", Critical},
				"data-den/deploy":                          {"FAILED: 1.2.4, ", Warning},
			},
			absent: []string{"smart-checks/disks-health"},
		},
		{
			name:   "Windows stopped checking, no backup for days",
			mdstat: healthyRAID,
			state: map[string]string{
				diskHealthFile: "status=ok\nchecked=" + old + "\n",
				backupFile:     "result=success\nfinished=" + old + "\nsuccess=" + old + "\n",
			},
			want: map[string][2]string{
				"smart-checks/disks-health": {unknown, ""},
				"data-den/disk-check":       {"no check since 2026-11-15 12:00 UTC", Warning},
				"data-den/backup":           {"no backup since 2026-11-15 12:00 UTC", Warning},
			},
		},
		{
			name:   "files the VM wrote are not understood",
			mdstat: healthyRAID,
			state: map[string]string{
				diskHealthFile: "garbage\n",
				backupFile:     "result=success\n",
			},
			want: map[string][2]string{
				"data-den/disk-check": {"not understood", Warning},
				"data-den/backup":     {"not understood", Warning},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := items(setup(t, tt.mdstat, tt.state))
			for key, want := range tt.want {
				it, ok := got[key]
				switch {
				case !ok:
					t.Errorf("%s missing; have %v", key, keys(got))
				case !strings.HasPrefix(it.Text, want[0]) || it.Alert != want[1]:
					t.Errorf("%s = %q (alert %q), want %q (alert %q)", key, it.Text, it.Alert, want[0], want[1])
				}
			}
			for _, key := range tt.absent {
				if _, ok := got[key]; ok {
					t.Errorf("%s is there, want it left out", key)
				}
			}
			for key, it := range got {
				if !validID(strings.SplitN(key, "/", 2)[1]) {
					t.Errorf("%s: id den-watcher would drop", key)
				}
				if strings.HasPrefix(key, checksGroup+"/") && (!strings.HasSuffix(it.ID, "-health") || it.Section == "") {
					t.Errorf("%s: a check needs an id ending in -health and a section", key)
				}
			}
		})
	}
}

func TestBackupAge(t *testing.T) {
	success := now.Add(-90 * time.Minute).Format(time.RFC3339)
	m := setup(t, healthyRAID, map[string]string{backupFile: "result=success\nfinished=" + success + "\nsuccess=" + success + "\n"})
	it := items(m)["data-den/backup-age"]
	if it.Value == nil || *it.Value != 1.5 || !it.History || it.Unit != "number" {
		t.Errorf("backup-age %+v, want 1.5 hours with its history", it)
	}
}

func TestFilesNotReady(t *testing.T) {
	m := setup(t, healthyRAID, nil)
	m.cfg.DataReady = func() bool { return false }
	if it := items(m)["data-den/files"]; it.Alert != Critical {
		t.Errorf("files %+v, want a critical alert", it)
	}
}

// validID is den-watcher's rule for ids: lowercase letters, digits, - and _,
// up to 40 characters.
func validID(id string) bool {
	return id != "" && len(id) <= 40 && strings.Trim(id, "abcdefghijklmnopqrstuvwxyz0123456789-_") == ""
}

func keys(m map[string]Item) []string {
	var list []string
	for k := range m {
		list = append(list, k)
	}
	return list
}
