package vision

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
)

// Label is a kind of photo, recognised by comparing the photo with Prompt.
type Label struct {
	// Name is shown in the web interface; "" for a label that only takes
	// photos away from the others, such as "a photo".
	Name   string
	Prompt string
}

// Labels are the things recognised. CLIP compares a photo with each prompt;
// the labels it is surest about are the photo's.
var Labels = []Label{
	{"Hunde", "a photo of a dog"},
	{"Katzen", "a photo of a cat"},
	{"Pferde", "a photo of a horse"},
	{"Vögel", "a photo of a bird"},
	{"Babys", "a photo of a baby"},
	{"Gruppenfotos", "a group photo of many people"},
	{"Strand", "a photo of a beach"},
	{"Meer und Seen", "a photo of the sea or a lake"},
	{"Berge", "a photo of mountains"},
	{"Schnee", "a photo of snow"},
	{"Wald", "a photo of a forest"},
	{"Sonnenuntergang", "a photo of a sunset"},
	{"Blumen", "a photo of flowers"},
	{"Städte", "a photo of a city street"},
	{"Gebäude", "a photo of a famous building"},
	{"Essen", "a photo of food on a plate"},
	{"Autos", "a photo of a car"},
	{"Fahrräder", "a photo of a bicycle"},
	{"Flugzeuge", "a photo of an airplane"},
	{"Feiern", "a photo of a party"},
	{"Weihnachten", "a photo of a christmas tree"},
	{"Geburtstag", "a photo of a birthday cake"},
	{"Sport", "a photo of people doing sports"},
	{"Dokumente", "a photo of a document or a receipt"},
	{"Bildschirmfotos", "a screenshot of a phone or computer screen"},
	// Photos of nothing in particular go here, not to a label above.
	{"", "a photo"},
	{"", "a photo of a person"},
	{"", "a photo of a room"},
	{"", "a photo of an object"},
}

const (
	// logitScale is CLIP's: it turns similarities into odds.
	logitScale = 100
	// minShare is the share of the odds a label needs.
	minShare = 0.3
	// maxLabels is how many labels a photo gets at most.
	maxLabels = 2
)

// labelsOf are the names of the labels for the image embedding, compared
// with texts, the embeddings of Labels' prompts.
func labelsOf(image []float32, texts [][]float32) []string {
	type scored struct {
		label int
		share float64
	}
	scores := make([]scored, len(texts))
	best := math.Inf(-1)
	for i, t := range texts {
		s := Similarity(image, t) * logitScale
		scores[i] = scored{i, s}
		best = math.Max(best, s)
	}
	var total float64
	for i := range scores {
		scores[i].share = math.Exp(scores[i].share - best)
		total += scores[i].share
	}
	sort.SliceStable(scores, func(a, b int) bool { return scores[a].share > scores[b].share })
	var names []string
	for _, s := range scores {
		if s.share/total < minShare || len(names) == maxLabels {
			break
		}
		if name := Labels[s.label].Name; name != "" {
			names = append(names, name)
		}
	}
	return names
}

// labelsVersion changes when a label or the way they are chosen changes.
func labelsVersion() string {
	h := sha256.New()
	for _, l := range Labels {
		h.Write([]byte(l.Name + "\x00" + l.Prompt + "\x00"))
	}
	h.Write([]byte{logitScale, byte(minShare * 100), maxLabels})
	return hex.EncodeToString(h.Sum(nil))[:8]
}
