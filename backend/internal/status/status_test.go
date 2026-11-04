package status

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

const (
	healthy = `Personalities : [raid1] [linear] [multipath] [raid0] [raid6] [raid5] [raid4] [raid10]
md127 : active raid1 sdc[1] sdb[0]
      3906886464 blocks super 1.2 [2/2] [UU]
      bitmap: 0/30 pages [0KB], 65536KB chunk

unused devices: <none>
`
	resyncing = `Personalities : [raid1]
md127 : active raid1 sdc[1] sdb[0]
      3906886464 blocks super 1.2 [2/2] [UU]
      [=>...................]  resync =  8.5% (332108160/3906886464) finish=330.4min speed=180264K/sec
      bitmap: 30/30 pages [120KB], 65536KB chunk

unused devices: <none>
`
	degraded = `Personalities : [raid1]
md127 : active raid1 sdc[1](F) sdb[0]
      3906886464 blocks super 1.2 [2/1] [U_]
      bitmap: 12/30 pages [48KB], 65536KB chunk

md1 : active (auto-read-only) raid1 sdd[2] sde[0]
      1048576 blocks super 1.2 [2/1] [U_]
      [====>................]  recovery = 23.0% (241172/1048576) finish=1.2min speed=10000K/sec

md2 : inactive sdf[0](S)
      976630464 blocks super 1.2

unused devices: <none>
`
)

func TestParseMDStat(t *testing.T) {
	tests := []struct {
		name, content string
		want          []Array
	}{
		{"no RAID", "Personalities : \nunused devices: <none>\n", nil},
		{"healthy", healthy, []Array{{Name: "md127", Level: "raid1", Active: true, Disks: 2, Working: 2, Healthy: true}}},
		{"first sync", resyncing, []Array{{Name: "md127", Level: "raid1", Active: true, Disks: 2, Working: 2, Sync: "resync", Progress: 8.5, Healthy: true}}},
		{"failed disk", degraded, []Array{
			{Name: "md127", Level: "raid1", Active: true, Disks: 2, Working: 1, Failed: []string{"sdc"}},
			{Name: "md1", Level: "raid1", Active: true, Disks: 2, Working: 1, Sync: "recovery", Progress: 23},
			{Name: "md2"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseMDStat(tt.content); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestDiskLevel(t *testing.T) {
	const tb = int64(1) << 40
	tests := []struct {
		free int64
		warn float64
		want string
	}{
		{tb / 2, 10, LevelOK},
		{tb/10 + 1, 10, LevelOK},
		{tb/10 - 1, 10, LevelLow},
		{tb / 50, 10, LevelCritical},
		{0, 10, LevelCritical},
		{tb / 5, 25, LevelLow},
	}
	for _, tt := range tests {
		if got := newDisk(tb, tt.free, tt.warn); got.Level != tt.want {
			t.Errorf("free %d of %d at %v%%: level %q, want %q", tt.free, tb, tt.warn, got.Level, tt.want)
		}
	}
	if d := newDisk(tb, tb/4, 10); d.Used != tb-tb/4 || d.FreePercent != 25 {
		t.Errorf("disk %+v", d)
	}
}

func TestWarnPercent(t *testing.T) {
	for in, want := range map[string]float64{"": DefaultWarnPercent, "5": 5, "12.5": 12.5} {
		if got, err := WarnPercent(in); err != nil || got != want {
			t.Errorf("WarnPercent(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "-1", "100", "ten", "10%"} {
		if _, err := WarnPercent(in); err == nil {
			t.Errorf("WarnPercent(%q): no error", in)
		}
	}
}

func TestBooted(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if got, want := booted("3600.42 7000.00\n", now), now.Add(-time.Hour-time.Second); !got.Equal(want) {
		t.Errorf("booted %v, want %v", got, want)
	}
	if got := booted("garbage", now); !got.IsZero() {
		t.Errorf("booted %v for garbage, want zero", got)
	}
}

func TestAPI(t *testing.T) {
	dir := t.TempDir()
	mdstat := filepath.Join(dir, "mdstat")
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(mdstat, []byte(degraded), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(uptime, []byte("60.00 120.00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	s := New(Config{DataDir: dir, MDStat: mdstat, Uptime: uptime, Version: "1.2.3", Started: started})
	mux := http.NewServeMux()
	loggedIn := false
	s.Register(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !loggedIn {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("without login: status %d, want 401", rec.Code)
	}

	loggedIn = true
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var r Report
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Version != "1.2.3" || !r.Started.Equal(started) || r.Booted.IsZero() {
		t.Errorf("report %+v", r)
	}
	if r.Disk == nil || r.Disk.Total <= 0 || r.Disk.WarnPercent != DefaultWarnPercent {
		t.Errorf("disk %+v, error %q", r.Disk, r.DiskError)
	}
	if len(r.RAID) != 3 || r.RAID[0].Healthy {
		t.Errorf("raid %+v", r.RAID)
	}
}

func TestMissingFiles(t *testing.T) {
	r := New(Config{DataDir: filepath.Join(t.TempDir(), "gone"), MDStat: "/nonexistent/mdstat", Uptime: "/nonexistent/uptime"}).Report()
	if r.Disk != nil || r.DiskError == "" || r.RAID != nil || !r.Booted.IsZero() {
		t.Errorf("report %+v", r)
	}
}
