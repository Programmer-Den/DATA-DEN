package status

import (
	"regexp"
	"strconv"
	"strings"
)

// Array is one RAID array from /proc/mdstat.
type Array struct {
	// Name is the kernel's name, such as "md127".
	Name string `json:"name"`
	// Level is the RAID level, such as "raid1".
	Level string `json:"level"`
	// Active is false for an array the kernel has stopped ("inactive").
	Active bool `json:"active"`
	// Disks is how many disks the array should have, Working how many it has.
	Disks   int `json:"disks"`
	Working int `json:"working"`
	// Failed are the disks marked as failed, such as "sdb".
	Failed []string `json:"failed,omitempty"`
	// Sync is what the array is doing, such as "resync" or "recovery",
	// empty when it is idle; Progress is how far, in percent.
	Sync     string  `json:"sync,omitempty"`
	Progress float64 `json:"progress,omitempty"`
	// Healthy is true when the array is active and has all its disks.
	Healthy bool `json:"healthy"`
}

var (
	arrayLine = regexp.MustCompile(`^(md\S*) : (active|inactive)(?: \([^)]*\))?(?: (raid\d+|linear|multipath))?(.*)$`)
	countRe   = regexp.MustCompile(`\[(\d+)/(\d+)\]`)
	syncRe    = regexp.MustCompile(`\b(resync|recovery|reshape|check|repair)\s*=\s*([\d.]+)%`)
	memberRe  = regexp.MustCompile(`^([^\s\[]+)\[\d+\](\([A-Z]\))*$`)
)

// ParseMDStat reads the arrays from the content of /proc/mdstat. It returns
// nil when there is none.
func ParseMDStat(content string) []Array {
	var arrays []Array
	var cur *Array
	for line := range strings.SplitSeq(content, "\n") {
		if m := arrayLine.FindStringSubmatch(line); m != nil {
			arrays = append(arrays, Array{Name: m[1], Active: m[2] == "active", Level: m[3]})
			cur = &arrays[len(arrays)-1]
			for member := range strings.FieldsSeq(m[4]) {
				mm := memberRe.FindStringSubmatch(member)
				if mm != nil && strings.Contains(member, "(F)") {
					cur.Failed = append(cur.Failed, mm[1])
				}
			}
			continue
		}
		if cur == nil {
			continue
		}
		if strings.TrimSpace(line) == "" {
			cur = nil
			continue
		}
		if m := countRe.FindStringSubmatch(line); m != nil && cur.Disks == 0 {
			cur.Disks, _ = strconv.Atoi(m[1])
			cur.Working, _ = strconv.Atoi(m[2])
		}
		if m := syncRe.FindStringSubmatch(line); m != nil {
			cur.Sync = m[1]
			cur.Progress, _ = strconv.ParseFloat(m[2], 64)
		}
	}
	for i := range arrays {
		a := &arrays[i]
		a.Healthy = a.Active && a.Disks > 0 && a.Working == a.Disks && len(a.Failed) == 0
	}
	return arrays
}
