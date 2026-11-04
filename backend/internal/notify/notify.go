// Package notify tells the users about what needs them, by email and by web
// push to their browsers and installed apps. Each user chooses which events
// reach them on which way:
//
//	GET  /api/notifications                          -> the user's settings and what the server can send
//	POST /api/notifications {"email", "events"}      -> 200, the settings saved
//	POST /api/notifications/subscribe {subscription} -> 200, push to this browser
//	POST /api/notifications/unsubscribe {"endpoint"} -> 200, no more push to it
//	POST /api/notifications/test {}                  -> 200, a test on every way the user set up
//
// "events" maps each kind, such as "disk", to {"email": bool, "push": bool}.
// The settings are kept in the data directory, in a folder the API never
// serves, next to the server's VAPID key for web push.
package notify

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Kind is a sort of event.
type Kind string

// The events. The system ones are about the NAS as a whole.
const (
	KindDisk    Kind = "disk"
	KindBackup  Kind = "backup"
	KindSpace   Kind = "space"
	KindCorrupt Kind = "corrupt"
	KindShare   Kind = "share"
)

// kindInfo describes a kind for the settings page.
type kindInfo struct {
	Kind   Kind   `json:"kind"`
	Name   string `json:"name"`
	System bool   `json:"system"`
}

// kinds are all kinds, in the order the settings page lists them.
var kinds = []kindInfo{
	{KindDisk, "Platte oder RAID fällt aus", true},
	{KindBackup, "Backup fehlgeschlagen", true},
	{KindSpace, "Wenig Speicherplatz", true},
	{KindCorrupt, "Beschädigte Dateien", true},
	{KindShare, "Etwas wurde mit mir geteilt", false},
}

func lookup(k Kind) (kindInfo, bool) {
	i := slices.IndexFunc(kinds, func(info kindInfo) bool { return info.Kind == k })
	if i < 0 {
		return kindInfo{}, false
	}
	return kinds[i], true
}

// MaxDevices is how many browsers one user can get push messages on.
const MaxDevices = 10

// Message is one notification.
type Message struct {
	Kind  Kind
	Title string
	Body  string
}

// Ways says how an event reaches the user.
type Ways struct {
	Email bool `json:"email"`
	Push  bool `json:"push"`
}

// Settings are one user's choices.
type Settings struct {
	Email         string         `json:"email"`
	Events        map[Kind]Ways  `json:"events"`
	Subscriptions []Subscription `json:"subscriptions,omitempty"`
}

// Config is what the notifications need.
type Config struct {
	// Dir keeps the settings and the VAPID key; empty keeps the settings in
	// memory and turns web push off.
	Dir string
	// Mail is the mail server; nil turns email off.
	Mail *Mail
	// PushContact is the sender's contact the push services see, a mailto:
	// or https: address; empty turns web push off.
	PushContact string
	// User is the logged-in account of a request that passed the login.
	User func(*http.Request) string
	// System tells whether an account gets the system events.
	System func(name string) bool
	// Client sends the push messages; nil means a client with a timeout.
	Client *http.Client
	// SendMail sends an email; nil sends it over Mail.
	SendMail func(to string, msg []byte) error
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// PushContact reads DATA_DEN_PUSH_CONTACT's value s, the contact the push
// services see. Empty means the sender of the emails, if there are emails.
func PushContact(s string, m *Mail) (string, error) {
	if s == "" {
		if m == nil {
			return "", nil
		}
		return "mailto:" + m.Address(), nil
	}
	if !strings.HasPrefix(s, "mailto:") && !strings.HasPrefix(s, "https://") {
		return "", errors.New("DATA_DEN_PUSH_CONTACT must start with mailto: or https://, such as mailto:nas@example.org")
	}
	return s, nil
}

// Notifier sends the notifications and serves their settings.
type Notifier struct {
	cfg   Config
	key   *ecdsa.PrivateKey
	mu    sync.Mutex
	users map[string]*Settings
	// sending are the messages on their way, for Wait.
	sending sync.WaitGroup
}

// New loads the settings from cfg.Dir.
func New(cfg Config) (*Notifier, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.SendMail == nil && cfg.Mail != nil {
		m := cfg.Mail
		cfg.SendMail = func(to string, msg []byte) error { return sendMail(m, to, msg) }
	}
	if cfg.System == nil {
		cfg.System = func(string) bool { return false }
	}
	n := &Notifier{cfg: cfg, users: map[string]*Settings{}}
	if cfg.Dir == "" {
		return n, nil
	}
	data, err := os.ReadFile(filepath.Clean(n.settingsFile()))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &n.users); err != nil {
			return nil, errors.New(n.settingsFile() + " is not valid JSON: " + err.Error())
		}
	}
	if cfg.PushContact != "" {
		if n.key, err = loadKey(filepath.Join(cfg.Dir, "vapid.pem")); err != nil {
			return nil, err
		}
	}
	return n, nil
}

func (n *Notifier) settingsFile() string {
	return filepath.Join(n.cfg.Dir, "settings.json")
}

// pushOn tells whether the server can send web push.
func (n *Notifier) pushOn() bool {
	return n.key != nil
}

// save writes the settings. The caller holds mu.
func (n *Notifier) save() error {
	if n.cfg.Dir == "" {
		return nil
	}
	data, err := json.MarshalIndent(n.users, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(n.cfg.Dir, 0o700); err != nil {
		return err
	}
	tmp := n.settingsFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, n.settingsFile())
}

// allowed tells whether user may get events of kind k.
func (n *Notifier) allowed(user string, k Kind) bool {
	info, ok := lookup(k)
	return ok && (!info.System || n.cfg.System(user))
}

// Notify sends m to user, the ways the user chose for its kind. It returns at
// once; the sending happens in the background.
func (n *Notifier) Notify(user string, m Message) {
	if n == nil || !n.allowed(user, m.Kind) {
		return
	}
	n.mu.Lock()
	s, ok := n.users[user]
	var ways Ways
	if ok {
		ways = s.Events[m.Kind]
	}
	n.mu.Unlock()
	if ways.Email || ways.Push {
		n.sending.Go(func() { n.deliver(user, m, ways) })
	}
}

// NotifySystem sends m to every account that gets the system events.
func (n *Notifier) NotifySystem(m Message) {
	if n == nil {
		return
	}
	n.mu.Lock()
	users := make([]string, 0, len(n.users))
	for user := range n.users {
		users = append(users, user)
	}
	n.mu.Unlock()
	for _, user := range users {
		n.Notify(user, m)
	}
}

// Wait waits until the messages on their way are sent.
func (n *Notifier) Wait() {
	n.sending.Wait()
}

// Forget drops the settings of a deleted account.
func (n *Notifier) Forget(user string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.users[user]; !ok {
		return
	}
	delete(n.users, user)
	if err := n.save(); err != nil {
		slog.Error("saving the notification settings", "err", err)
	}
}

// result is what a delivery did, for the test message.
type result struct {
	Email   string `json:"email,omitempty"`
	Devices int    `json:"devices"`
	Failed  int    `json:"failed"`
}

// deliver sends m to user over ways.
func (n *Notifier) deliver(user string, m Message, ways Ways) result {
	var r result
	n.mu.Lock()
	s := n.users[user]
	var email string
	var subs []Subscription
	if s != nil {
		email = s.Email
		subs = slices.Clone(s.Subscriptions)
	}
	n.mu.Unlock()
	if ways.Email && email != "" && n.cfg.SendMail != nil {
		if err := n.mail(email, m); err != nil {
			slog.Error("sending a notification by email", "username", user, "kind", m.Kind, "err", err)
			r.Email = "Senden fehlgeschlagen: " + err.Error()
		} else {
			r.Email = "ok"
		}
	}
	if ways.Push && n.pushOn() {
		payload := pushPayload(m)
		info, _ := lookup(m.Kind)
		for _, sub := range subs {
			err := n.push(sub, payload, info.System)
			switch {
			case errors.Is(err, errGone):
				slog.Info("a browser dropped its push subscription", "username", user)
				n.drop(user, sub.Endpoint)
				r.Failed++
			case err != nil:
				slog.Error("sending a push notification", "username", user, "kind", m.Kind, "err", err)
				r.Failed++
			default:
				r.Devices++
			}
		}
	}
	return r
}

func (n *Notifier) mail(to string, m Message) error {
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	host := "data-den"
	if n.cfg.Mail != nil {
		if _, domain, ok := strings.Cut(n.cfg.Mail.Address(), "@"); ok {
			host = domain
		}
	}
	from := "DATA-DEN"
	if n.cfg.Mail != nil {
		from = n.cfg.Mail.From
	}
	body := m.Body + "\n\n-- \nDATA-DEN. Welche Nachrichten du bekommst, stellst du in DATA-DEN unter Benachrichtigungen ein.\n"
	return n.cfg.SendMail(to, buildMail(from, to, "DATA-DEN: "+m.Title, body, n.cfg.Now(), hex.EncodeToString(id)+"@"+host))
}

// pushPayload is what the service worker gets, cut to what a push service
// takes.
func pushPayload(m Message) []byte {
	body := m.Body
	for {
		data, _ := json.Marshal(map[string]string{"title": m.Title, "body": body, "tag": string(m.Kind)})
		if len(data) <= maxPayload || body == "" {
			return data
		}
		cut := []rune(body)
		body = string(cut[:len(cut)*3/4]) + "…"
	}
}

// drop removes the subscription endpoint of user.
func (n *Notifier) drop(user, endpoint string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.users[user]
	if s == nil {
		return
	}
	s.Subscriptions = slices.DeleteFunc(s.Subscriptions, func(sub Subscription) bool { return sub.Endpoint == endpoint })
	if err := n.save(); err != nil {
		slog.Error("saving the notification settings", "err", err)
	}
}

// Register adds the settings API to mux, behind require (the login check).
func (n *Notifier) Register(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/notifications", require(n.show))
	mux.HandleFunc("POST /api/notifications", require(n.change))
	mux.HandleFunc("POST /api/notifications/subscribe", require(n.subscribe))
	mux.HandleFunc("POST /api/notifications/unsubscribe", require(n.unsubscribe))
	mux.HandleFunc("POST /api/notifications/test", require(n.test))
}

// view is the answer of GET /api/notifications.
type view struct {
	Email  string        `json:"email"`
	Events map[Kind]Ways `json:"events"`
	// Kinds are the events this user can choose.
	Kinds []kindInfo `json:"kinds"`
	// Mail tells whether the server can send email.
	Mail bool `json:"mail"`
	// PushKey is the server's VAPID key for subscribing, empty without push.
	PushKey string `json:"pushKey"`
	// Devices are the endpoints that get push messages.
	Devices []string `json:"devices"`
}

func (n *Notifier) view(user string) view {
	v := view{Events: map[Kind]Ways{}, Kinds: []kindInfo{}, Mail: n.cfg.SendMail != nil, Devices: []string{}}
	if n.pushOn() {
		v.PushKey = base64.RawURLEncoding.EncodeToString(publicKeyBytes(n.key))
	}
	for _, info := range kinds {
		if n.allowed(user, info.Kind) {
			v.Kinds = append(v.Kinds, info)
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if s := n.users[user]; s != nil {
		v.Email = s.Email
		for k, ways := range s.Events {
			if n.allowed(user, k) {
				v.Events[k] = ways
			}
		}
		for _, sub := range s.Subscriptions {
			v.Devices = append(v.Devices, sub.Endpoint)
		}
	}
	return v
}

func (n *Notifier) show(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, n.view(n.cfg.User(r)))
}

func (n *Notifier) change(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email  string        `json:"email"`
		Events map[Kind]Ways `json:"events"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	user := n.cfg.User(r)
	body.Email = strings.TrimSpace(body.Email)
	if body.Email != "" && !validEmail(body.Email) {
		writeError(w, http.StatusBadRequest, "Ungültige E-Mail-Adresse")
		return
	}
	events := map[Kind]Ways{}
	for k, ways := range body.Events {
		if !n.allowed(user, k) {
			writeError(w, http.StatusBadRequest, "Unbekannte Benachrichtigung")
			return
		}
		if ways.Email || ways.Push {
			events[k] = ways
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.users[user]
	if s == nil {
		s = &Settings{}
		n.users[user] = s
	}
	s.Email, s.Events = body.Email, events
	if err := n.save(); err != nil {
		slog.Error("saving the notification settings", "err", err)
		writeError(w, http.StatusInternalServerError, "Speichern fehlgeschlagen")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{})
}

func (n *Notifier) subscribe(w http.ResponseWriter, r *http.Request) {
	if !n.pushOn() {
		writeError(w, http.StatusConflict, "Push-Nachrichten sind auf dem Server nicht eingerichtet")
		return
	}
	var sub Subscription
	if !readJSON(w, r, &sub) {
		return
	}
	if err := sub.check(); err != nil {
		writeError(w, http.StatusBadRequest, "Ungültiges Push-Abonnement")
		return
	}
	sub.Created = n.cfg.Now().UTC()
	user := n.cfg.User(r)
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.users[user]
	if s == nil {
		s = &Settings{Events: map[Kind]Ways{}}
		n.users[user] = s
	}
	s.Subscriptions = slices.DeleteFunc(s.Subscriptions, func(old Subscription) bool { return old.Endpoint == sub.Endpoint })
	s.Subscriptions = append(s.Subscriptions, sub)
	if len(s.Subscriptions) > MaxDevices {
		// The oldest browser makes room.
		s.Subscriptions = s.Subscriptions[len(s.Subscriptions)-MaxDevices:]
	}
	if err := n.save(); err != nil {
		slog.Error("saving the notification settings", "err", err)
		writeError(w, http.StatusInternalServerError, "Speichern fehlgeschlagen")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{})
}

func (n *Notifier) unsubscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	n.drop(n.cfg.User(r), body.Endpoint)
	writeJSON(w, http.StatusOK, map[string]string{})
}

func (n *Notifier) test(w http.ResponseWriter, r *http.Request) {
	user := n.cfg.User(r)
	res := n.deliver(user, Message{
		Kind:  KindShare,
		Title: "Testnachricht",
		Body:  "So sehen Benachrichtigungen von DATA-DEN aus.",
	}, Ways{Email: true, Push: true})
	writeJSON(w, http.StatusOK, res)
}

// readJSON reads a small JSON body into v. It answers the request itself and
// returns false when that fails.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	// A JSON body cannot be sent by a plain HTML form on another site.
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "JSON erwartet")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
