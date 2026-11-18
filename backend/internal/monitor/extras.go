package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/status"
)

// Extra is a group of values den-watcher shows as a card of its own, in the
// fields of its extras (docs/data.md, "Extras").
type Extra struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Titles       map[string]string `json:"titles,omitempty"`
	Description  string            `json:"description,omitempty"`
	Descriptions map[string]string `json:"descriptions,omitempty"`
	Items        []Item            `json:"items"`
}

// Item is one value of an Extra.
type Item struct {
	ID      string            `json:"id"`
	Label   string            `json:"label"`
	Labels  map[string]string `json:"labels,omitempty"`
	Section string            `json:"section,omitempty"`
	Unit    string            `json:"unit"`
	Value   *float64          `json:"value,omitempty"`
	Text    string            `json:"text,omitempty"`
	Texts   map[string]string `json:"texts,omitempty"`
	History bool              `json:"history,omitempty"`
	// Alert is "warning" or "critical" while the value needs attention.
	// den-watcher does not read it yet; a hub that does can raise an
	// alert for any value without knowing it.
	Alert string `json:"alert,omitempty"`
}

// Alert levels of an Item.
const (
	Warning  = "warning"
	Critical = "critical"
)

// The marks of den-watcher's smart add-on: a hub raises an alert for every
// value of the group "smart-checks" whose id ends in "-health" and whose
// text is failed.
const (
	checksGroup = "smart-checks"
	passed      = "✓"
	failed      = "✗"
	unknown     = "–"
)

// Files the VM's scripts leave in Config.StateDir.
const (
	diskHealthFile = "disk-health-report" // vm/disk-health.sh: what Windows handed over
	backupFile     = "backup-result"      // vm/backup-result.sh: the last run
	deployFile     = "deploy-result"      // vm/deploy.sh: the last deploy
)

// Windows checks the disks every 15 minutes; a check this old is reported,
// as vm/disk-health.sh does.
const diskCheckMaxAge = 24 * time.Hour

// The backup runs every night; without a successful run for this long it is
// reported.
const backupMaxAge = 48 * time.Hour

// timeFormat is how times are shown on the hub's page.
const timeFormat = "2006-01-02 15:04 UTC"

func (m *Monitor) extras(report status.Report) []Extra {
	now := m.now().UTC()
	checks := Extra{
		ID:     checksGroup,
		Title:  "Disks and RAID",
		Titles: map[string]string{"de": "Platten und RAID"},
		Description: "Whether every disk of the mirror is there and whether Windows finds the disks healthy (SMART, " +
			"read and write errors, temperature, wear). A failed check needs a look at the NAS.",
		Descriptions: map[string]string{"de": "Ob jede Platte des Spiegels da ist und ob Windows die Platten für gesund " +
			"hält (SMART, Lese- und Schreibfehler, Temperatur, Verschleiß). Eine nicht bestandene Prüfung braucht einen Blick auf das NAS."},
	}
	nas := Extra{
		ID:     "data-den",
		Title:  "DATA-DEN",
		Titles: map[string]string{"de": "DATA-DEN"},
		Description: "The NAS itself: whether its files can be read, the disk check from Windows, the nightly backup " +
			"and the last deploy.",
		Descriptions: map[string]string{"de": "Das NAS selbst: ob seine Dateien lesbar sind, die Plattenprüfung von Windows, " +
			"das nächtliche Backup und der letzte Deploy."},
	}
	if m.cfg.Version != "" {
		nas.Items = append(nas.Items, text("version", "Version", "Version", m.cfg.Version, m.cfg.Version, ""))
	}
	if m.cfg.DataReady != nil {
		if m.cfg.DataReady() {
			nas.Items = append(nas.Items, text("files", "Files", "Dateien", "ready", "bereit", ""))
		} else {
			nas.Items = append(nas.Items, text("files", "Files", "Dateien", "cannot be read: is the mirror unlocked?", "nicht lesbar: ist der Spiegel entsperrt?", Critical))
		}
	}
	for _, a := range report.RAID {
		checks.Items = append(checks.Items, raidItem(a))
	}
	if m.cfg.StateDir != "" {
		diskItems, diskCheck := m.diskHealth(now)
		checks.Items = append(checks.Items, diskItems...)
		if diskCheck != nil {
			nas.Items = append(nas.Items, *diskCheck)
		}
		nas.Items = append(nas.Items, m.backup(now)...)
		if deploy := m.deploy(); deploy != nil {
			nas.Items = append(nas.Items, *deploy)
		}
	}
	var extras []Extra
	for _, g := range []Extra{checks, nas} {
		if len(g.Items) > 0 {
			extras = append(extras, g)
		}
	}
	return extras
}

// text is a value of the unit text, in English and German.
func text(id, label, labelDE, value, valueDE, alert string) Item {
	return Item{
		ID: id, Label: label, Labels: map[string]string{"de": labelDE},
		Unit: "text", Text: value, Texts: map[string]string{"de": valueDE}, Alert: alert,
	}
}

// check is a value of the group checksGroup, shown under section.
func check(id, section, label, labelDE, mark, alert string) Item {
	return Item{
		ID: id + "-health", Section: section,
		Label: section + ": " + label, Labels: map[string]string{"de": section + ": " + labelDE},
		Unit: "text", Text: mark, Alert: alert,
	}
}

func raidItem(a status.Array) Item {
	section := "RAID " + a.Name
	id := "raid-" + idOf(a.Name)
	switch {
	case !a.Active:
		return check(id, section, "STOPPED", "ANGEHALTEN", failed, Critical)
	case !a.Healthy:
		missing := a.Disks - a.Working
		label := fmt.Sprintf("%d of %d disks MISSING", missing, a.Disks)
		labelDE := fmt.Sprintf("%d von %d Platten FEHLEN", missing, a.Disks)
		if a.Sync != "" {
			label += fmt.Sprintf(", %s %.1f %%", a.Sync, a.Progress)
			labelDE += fmt.Sprintf(", %s %.1f %%", a.Sync, a.Progress)
		}
		return check(id, section, label, labelDE, failed, Critical)
	case a.Sync != "":
		return check(id, section, fmt.Sprintf("complete, %s %.1f %%", a.Sync, a.Progress),
			fmt.Sprintf("vollständig, %s %.1f %%", a.Sync, a.Progress), passed, "")
	}
	return check(id, section, "complete", "vollständig", passed, "")
}

var (
	nonID    = regexp.MustCompile(`[^a-z0-9]+`)
	stateKey = regexp.MustCompile(`^[a-z_]+$`)
)

// idOf turns a name into a part of an id: lowercase letters, digits and -.
func idOf(name string) string {
	id := strings.Trim(nonID.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(id) > 24 {
		id = strings.TrimRight(id[:24], "-")
	}
	if id == "" {
		return "x"
	}
	return id
}

// readState returns the "key=value" lines of a file in the state directory,
// the other lines, and when it was written; ok is false when it is not
// there.
func (m *Monitor) readState(name string) (values map[string]string, rest []string, modified time.Time, ok bool) {
	path := filepath.Join(m.cfg.StateDir, name)
	info, err := os.Stat(path)
	if err != nil || info.Size() > 64<<10 {
		return nil, nil, time.Time{}, false
	}
	data, err := os.ReadFile(path) //nolint:gosec // a file of the VM's state directory, by a fixed name
	if err != nil {
		return nil, nil, time.Time{}, false
	}
	values = map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found && stateKey.MatchString(key) {
			if _, seen := values[key]; !seen {
				values[key] = value
			}
			continue
		}
		if line = strings.TrimSpace(line); line != "" {
			rest = append(rest, line)
		}
	}
	return values, rest, info.ModTime(), true
}

// diskHealth reads what Windows found about the disks: a value of the group
// checksGroup for each disk with a problem, or one for all disks, and the
// state of the check itself. Nothing when the check is not set up.
func (m *Monitor) diskHealth(now time.Time) ([]Item, *Item) {
	values, problems, _, ok := m.readState(diskHealthFile)
	if !ok {
		return nil, nil
	}
	checked, err := time.Parse(time.RFC3339, values["checked"])
	if err != nil || (values["status"] != "ok" && values["status"] != "warn") {
		item := text("disk-check", "Disk check by Windows", "Plattenprüfung durch Windows", "not understood", "nicht verstanden", Warning)
		return nil, &item
	}
	when := checked.UTC().Format(timeFormat)
	if now.Sub(checked) > diskCheckMaxAge {
		item := text("disk-check", "Disk check by Windows", "Plattenprüfung durch Windows",
			"no check since "+when+": is the task \"DATA-DEN disk health\" running?",
			"keine Prüfung seit "+when+": läuft die Aufgabe \"DATA-DEN disk health\"?", Warning)
		return []Item{check("disks", "Windows", "not checked since "+when, "nicht geprüft seit "+when, unknown, "")}, &item
	}
	if values["status"] == "ok" {
		item := text("disk-check", "Disk check by Windows", "Plattenprüfung durch Windows", "all disks fine, "+when, "alle Platten in Ordnung, "+when, "")
		return []Item{check("disks", "Windows", "all disks fine", "alle Platten in Ordnung", passed, "")}, &item
	}
	var items []Item
	for _, line := range problems {
		if line == "..." {
			continue
		}
		// "Platte 2 (WDC, X1): 3 nicht korrigierbare Lesefehler; ..."
		disk, problem, found := strings.Cut(line, ": ")
		if !found {
			disk, problem = "Windows", line
		}
		items = append(items, check("disk-"+idOf(disk), disk, problem, problem, failed, Critical))
	}
	if len(items) == 0 {
		items = append(items, check("disks", "Windows", "a disk has problems", "eine Platte hat Probleme", failed, Critical))
	}
	item := text("disk-check", "Disk check by Windows", "Plattenprüfung durch Windows", "problems, "+when, "Probleme, "+when, Critical)
	return items, &item
}

// backup reads the result of the last backup run: how it went, and the hours
// since the last successful one. Nothing when the backup is off.
func (m *Monitor) backup(now time.Time) []Item {
	values, _, _, ok := m.readState(backupFile)
	if !ok {
		return nil
	}
	finished, err := time.Parse(time.RFC3339, values["finished"])
	if err != nil {
		return []Item{text("backup", "Backup", "Backup", "not understood", "nicht verstanden", Warning)}
	}
	when := finished.UTC().Format(timeFormat)
	var items []Item
	success, err := time.Parse(time.RFC3339, values["success"])
	switch {
	case values["result"] != "success":
		items = append(items, text("backup", "Backup", "Backup",
			"FAILED "+when+" ("+values["result"]+")", "FEHLGESCHLAGEN "+when+" ("+values["result"]+")", Critical))
	case err == nil && now.Sub(success) > backupMaxAge:
		items = append(items, text("backup", "Backup", "Backup", "no backup since "+when, "kein Backup seit "+when, Warning))
	default:
		items = append(items, text("backup", "Backup", "Backup", "fine, "+when, "in Ordnung, "+when, ""))
	}
	if err == nil {
		hours := float64(int(now.Sub(success).Hours()*10)) / 10
		items = append(items, Item{
			ID: "backup-age", Label: "Hours since the last backup", Labels: map[string]string{"de": "Stunden seit dem letzten Backup"},
			Unit: "number", Value: &hours, History: true,
		})
	}
	return items
}

// deploy reads the result of the last deploy from Windows. A failed one is a
// warning until the next one works, as DATA-DEN keeps running the version
// before it.
func (m *Monitor) deploy() *Item {
	values, _, modified, ok := m.readState(deployFile)
	if !ok {
		return nil
	}
	when := modified.UTC().Format(timeFormat)
	tag := values["tag"]
	var item Item
	if values["status"] == "ok" {
		item = text("deploy", "Last deploy", "Letzter Deploy", tag+", "+when, tag+", "+when, "")
	} else {
		item = text("deploy", "Last deploy", "Letzter Deploy", "FAILED: "+tag+", "+when, "FEHLGESCHLAGEN: "+tag+", "+when, Warning)
	}
	return &item
}
