package notify

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/status"
)

func subjects(f *fixture) []string {
	var out []string
	for _, m := range f.sent() {
		for line := range strings.SplitSeq(m.msg, "\r\n") {
			if s, ok := strings.CutPrefix(line, "Subject: "); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// has tells whether one of the subjects contains part.
func has(subjects []string, part string) bool {
	for _, s := range subjects {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

func TestWatcher(t *testing.T) {
	f := setup(t, t.TempDir())
	f.do(t, "chriszly", http.MethodPost, "/api/notifications", `{"email": "chris@example.org", "events": {"disk": {"email": true}, "space": {"email": true}, "backup": {"email": true}}}`)
	report := status.Report{
		RAID: []status.Array{{Name: "md0", Active: true, Disks: 2, Working: 2, Healthy: true}},
		Disk: &status.Disk{Free: 500e9, FreePercent: 50, Level: status.LevelOK},
	}
	events := filepath.Join(f.dir, "events")
	w := &Watcher{Report: func() status.Report { return report }, EventsDir: events}

	w.look(f.n)
	if s := subjects(f); len(s) != 0 {
		t.Fatalf("all fine, but sent %v", s)
	}
	report.RAID[0] = status.Array{Name: "md0", Active: true, Disks: 2, Working: 1, Failed: []string{"sdb"}}
	report.Disk = &status.Disk{Free: 30e9, FreePercent: 3, Level: status.LevelLow}
	w.look(f.n)
	w.look(f.n) // Not again while nothing changes.
	s := subjects(f)
	if len(s) != 2 || !has(s, "md0") || !has(s, "Wenig Speicherplatz") {
		t.Fatalf("sent %v", s)
	}
	if all := f.sent()[0].msg + f.sent()[1].msg; !strings.Contains(all, "1 von 2 Platten") || !strings.Contains(all, "sdb") {
		t.Errorf("mails %s", all)
	}

	report.RAID[0] = status.Array{Name: "md0", Active: true, Disks: 2, Working: 2, Healthy: true}
	report.Disk = &status.Disk{Free: 1e9, FreePercent: 0.1, Level: status.LevelCritical}
	w.look(f.n)
	s = subjects(f)[2:]
	if len(s) != 2 || !has(s, "wieder in Ordnung") || !has(s, "fast aufgebraucht") {
		t.Fatalf("sent %v", s)
	}

	// The VM's events are sent once and deleted; others are dropped.
	if err := os.MkdirAll(events, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(events, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("1.json", `{"kind": "backup", "title": "Backup fehlgeschlagen", "body": "restic: the repository is locked"}`)
	write("2.json", `{"kind": "share", "title": "made up"}`)
	write("3.json", `not json`)
	write("4.txt", `{"kind": "backup", "title": "not an event file"}`)
	w.look(f.n)
	s = subjects(f)[4:]
	if len(s) != 1 || !has(s, "Backup fehlgeschlagen") {
		t.Fatalf("sent %v", s)
	}
	left, _ := os.ReadDir(events)
	if len(left) != 1 || left[0].Name() != "4.txt" {
		t.Errorf("left %v", left)
	}
}
