package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/status"
)

// Watcher looks at the NAS every Interval and tells the system events: a disk
// of the RAID failing, the free space running low, and what the VM reports
// through EventsDir (a failed backup, a disk whose SMART values look bad).
//
// A message goes out when something gets worse, and once more when it is
// fine again, not on every look.
//
// The VM writes each event as a JSON file into EventsDir:
//
//	{"kind": "backup", "title": "Backup fehlgeschlagen", "body": "..."}
//
// The watcher sends it and deletes the file, so EventsDir must belong to the
// server's user.
type Watcher struct {
	// Report is the status of the NAS, normally status.Status.Report.
	Report func() status.Report
	// EventsDir is where the VM leaves its events; empty reads none.
	EventsDir string
	// Interval is how often it looks; 0 means a minute.
	Interval time.Duration

	// raid is whether each array was healthy at the last look.
	raid map[string]bool
	// level is the disk's level at the last look.
	level string
	// stuck are event files that could not be deleted, so they are not
	// sent again.
	stuck map[string]bool
}

// maxEventBytes is the largest event file read.
const maxEventBytes = 16 << 10

// vmKinds are the kinds the VM may report.
var vmKinds = []Kind{KindBackup, KindDisk}

// Run looks until ctx is done, telling n.
func (w *Watcher) Run(ctx context.Context, n *Notifier) {
	interval := w.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		w.look(n)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// look checks everything once.
func (w *Watcher) look(n *Notifier) {
	if w.Report != nil {
		r := w.Report()
		w.checkRAID(n, r.RAID)
		w.checkDisk(n, r.Disk)
	}
	if w.EventsDir != "" {
		w.readEvents(n)
	}
}

func (w *Watcher) checkRAID(n *Notifier, arrays []status.Array) {
	if w.raid == nil {
		w.raid = map[string]bool{}
	}
	for _, a := range arrays {
		was, known := w.raid[a.Name]
		w.raid[a.Name] = a.Healthy
		switch {
		case !a.Healthy && (was || !known):
			n.NotifySystem(Message{Kind: KindDisk, Title: "RAID " + a.Name + " beschädigt", Body: describeArray(a)})
		case a.Healthy && known && !was:
			n.NotifySystem(Message{
				Kind: KindDisk, Title: "RAID " + a.Name + " wieder in Ordnung",
				Body: fmt.Sprintf("Das RAID %s arbeitet wieder mit allen %d Platten.", a.Name, a.Disks),
			})
		}
	}
}

func describeArray(a status.Array) string {
	var b strings.Builder
	if !a.Active {
		fmt.Fprintf(&b, "Das RAID %s ist gestoppt; die Daten sind nicht erreichbar.", a.Name)
	} else {
		fmt.Fprintf(&b, "Das RAID %s arbeitet nur noch mit %d von %d Platten.", a.Name, a.Working, a.Disks)
	}
	if len(a.Failed) > 0 {
		fmt.Fprintf(&b, " Ausgefallen: %s.", strings.Join(a.Failed, ", "))
	}
	if a.Sync != "" {
		fmt.Fprintf(&b, " Gerade läuft %s (%.1f %%).", a.Sync, a.Progress)
	}
	b.WriteString(" Fällt noch eine Platte aus, sind die Daten verloren: tausche die Platte bald (vm/README.md).")
	return b.String()
}

// levels orders the disk levels from good to bad.
var levels = []string{status.LevelOK, status.LevelLow, status.LevelCritical}

func (w *Watcher) checkDisk(n *Notifier, d *status.Disk) {
	if d == nil {
		return
	}
	before := slices.Index(levels, w.level)
	if before < 0 {
		before = 0
	}
	now := slices.Index(levels, d.Level)
	w.level = d.Level
	switch {
	case now > before:
		title := "Wenig Speicherplatz"
		if d.Level == status.LevelCritical {
			title = "Speicherplatz fast aufgebraucht"
		}
		n.NotifySystem(Message{
			Kind: KindSpace, Title: title,
			Body: fmt.Sprintf("Auf DATA-DEN sind nur noch %s frei (%.1f %%). Lösche Dateien oder leere den Papierkorb.", formatBytes(d.Free), d.FreePercent),
		})
	case now == 0 && before > 0:
		n.NotifySystem(Message{
			Kind: KindSpace, Title: "Wieder genug Speicherplatz",
			Body: fmt.Sprintf("Auf DATA-DEN sind wieder %s frei (%.1f %%).", formatBytes(d.Free), d.FreePercent),
		})
	}
}

func formatBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(n)
	i := 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", v, units[i]), ".", ",", 1)
}

// event is a file the VM left in EventsDir.
type event struct {
	Kind  Kind   `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (w *Watcher) readEvents(n *Notifier) {
	entries, err := os.ReadDir(w.EventsDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("reading the events of the VM", "dir", w.EventsDir, "err", err)
		}
		return
	}
	if w.stuck == nil {
		w.stuck = map[string]bool{}
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasSuffix(name, ".json") || w.stuck[name] {
			continue
		}
		file := filepath.Join(w.EventsDir, name)
		e, err := readEvent(file)
		if err != nil {
			slog.Warn("an event of the VM is not understood", "file", file, "err", err)
		} else {
			n.NotifySystem(Message(e))
		}
		if err := os.Remove(file); err != nil {
			slog.Error("an event of the VM cannot be deleted, so it is not sent again until a restart", "file", file, "err", err)
			w.stuck[name] = true
		}
	}
}

func readEvent(file string) (event, error) {
	var e event
	info, err := os.Stat(file)
	if err != nil {
		return e, err
	}
	if info.Size() > maxEventBytes {
		return e, errors.New("too large")
	}
	data, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return e, err
	}
	if !slices.Contains(vmKinds, e.Kind) {
		return e, fmt.Errorf("unknown kind %q", e.Kind)
	}
	e.Title = oneLine(e.Title)
	if e.Title == "" {
		return e, errors.New("no title")
	}
	return e, nil
}
