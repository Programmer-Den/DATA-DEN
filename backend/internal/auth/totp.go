package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 and every authenticator app use HMAC-SHA1
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rsc.io/qr"
)

// Two-factor login with a code from an authenticator app (TOTP, RFC 6238),
// which each account turns on for itself:
//
//	GET  /api/account/2fa                  -> {"enabled", "recoveryLeft"}
//	POST /api/account/2fa/setup {}         -> {"secret", "uri", "qr"}, a new secret, not yet active
//	POST /api/account/2fa/enable {"code"}  -> {"recovery": [...]}, active once the app's code fits
//	POST /api/account/2fa/disable {"password", "code"} -> 200
//
// With it on, a login needs {"username", "password", "code"}. The code is
// one from the app or one of the ten recovery codes, each usable once. An
// admin can turn it off for another account whose phone is lost.

const (
	totpPeriod = 30
	totpDigits = 6
	// totpSkew is how many periods a code may be early or late, for clocks
	// that are a little off.
	totpSkew = 1
	// RecoveryCodes is how many recovery codes an account gets.
	RecoveryCodes = 10
	// issuer names DATA-DEN in the authenticator app.
	issuer = "DATA-DEN"
)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// TwoFactor is the second factor of one account.
type TwoFactor struct {
	// Secret is the shared key, base32 as the app shows it.
	Secret string `json:"secret"`
	// Last is the last time step a code was used for, so no code works twice.
	Last int64 `json:"last"`
	// Recovery holds the SHA-256 of each unused recovery code.
	Recovery []string `json:"recovery"`
}

// totpCode is the code for time step step.
func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step)) //nolint:gosec // steps are positive
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000)
}

// matchTOTP returns the time step code is valid for, after last, or false.
func matchTOTP(secretB32, code string, now time.Time, last int64) (int64, bool) {
	secret, err := base32NoPad.DecodeString(secretB32)
	if err != nil || len(code) != totpDigits {
		return 0, false
	}
	step := now.Unix() / totpPeriod
	for s := step - totpSkew; s <= step+totpSkew; s++ {
		if s > last && subtle.ConstantTimeCompare([]byte(totpCode(secret, s)), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}

func newSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32NoPad.EncodeToString(b), nil
}

// newRecoveryCodes returns codes such as "k7qmz-3xw2p" and their hashes.
func newRecoveryCodes() (codes, hashes []string, err error) {
	alphabet := "abcdefghjkmnpqrstuvwxyz23456789"
	for range RecoveryCodes {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		var code strings.Builder
		for i, c := range b {
			if i == 5 {
				code.WriteByte('-')
			}
			code.WriteByte(alphabet[int(c)%len(alphabet)])
		}
		codes = append(codes, code.String())
		hashes = append(hashes, hashRecovery(code.String()))
	}
	return codes, hashes, nil
}

// normalizeCode drops spaces and dashes, as people type codes either way.
func normalizeCode(code string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "-", "").Replace(code))
}

func hashRecovery(code string) string {
	sum := sha256.Sum256([]byte(normalizeCode(code)))
	return hex.EncodeToString(sum[:])
}

// checkSecondFactor checks code for the account name and uses it up: a
// time step is not accepted again, a recovery code is removed. The caller
// does not hold mu.
func (a *Auth) checkSecondFactor(name, code string) bool {
	code = normalizeCode(code)
	a.mu.Lock()
	defer a.mu.Unlock()
	tf, ok := a.twoFactor[name]
	if !ok {
		return false
	}
	if step, ok := matchTOTP(tf.Secret, code, a.cfg.Now(), tf.Last); ok {
		tf.Last = step
		a.twoFactor[name] = tf
		if err := a.save(); err != nil {
			slog.Error("saving the last code", "err", err)
		}
		return true
	}
	hash := hashRecovery(code)
	for i, h := range tf.Recovery {
		if subtle.ConstantTimeCompare([]byte(h), []byte(hash)) == 1 {
			tf.Recovery = append(tf.Recovery[:i:i], tf.Recovery[i+1:]...)
			a.twoFactor[name] = tf
			if err := a.save(); err != nil {
				slog.Error("saving the used recovery code", "err", err)
			}
			slog.Info("recovery code used", "username", name, "left", len(tf.Recovery))
			return true
		}
	}
	return false
}

// hasSecondFactor reports whether the account name needs a code to log in.
func (a *Auth) hasSecondFactor(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.twoFactor[name]
	return ok
}

func (a *Auth) registerTwoFactor(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/account/2fa", a.Require(a.twoFactorState))
	mux.HandleFunc("POST /api/account/2fa/setup", a.Require(a.setupTwoFactor))
	mux.HandleFunc("POST /api/account/2fa/enable", a.Require(a.enableTwoFactor))
	mux.HandleFunc("POST /api/account/2fa/disable", a.Require(a.disableTwoFactor))
}

func (a *Auth) twoFactorState(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	tf, ok := a.twoFactor[Username(r)]
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"enabled": ok, "recoveryLeft": len(tf.Recovery)})
}

func (a *Auth) setupTwoFactor(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !readJSON(w, r, &body) {
		return
	}
	name := Username(r)
	if a.hasSecondFactor(name) {
		writeError(w, http.StatusConflict, "Die Zwei-Faktor-Anmeldung ist schon an")
		return
	}
	secret, err := newSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Einrichten fehlgeschlagen")
		return
	}
	uri := (&url.URL{
		Scheme: "otpauth",
		Host:   "totp",
		Path:   "/" + issuer + ":" + name,
		RawQuery: url.Values{
			"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"},
			"digits": {fmt.Sprint(totpDigits)}, "period": {fmt.Sprint(totpPeriod)},
		}.Encode(),
	}).String()
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Einrichten fehlgeschlagen")
		return
	}
	a.mu.Lock()
	a.pendingSecrets[name] = pendingSecret{secret: secret, expires: a.cfg.Now().Add(10 * time.Minute)}
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": secret,
		"uri":    uri,
		"qr":     "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()),
	})
}

type pendingSecret struct {
	secret  string
	expires time.Time
}

func (a *Auth) enableTwoFactor(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := Username(r)
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Einrichten fehlgeschlagen")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	pending, ok := a.pendingSecrets[name]
	if !ok || !a.cfg.Now().Before(pending.expires) {
		delete(a.pendingSecrets, name)
		writeError(w, http.StatusBadRequest, "Bitte das Einrichten neu beginnen")
		return
	}
	step, ok := matchTOTP(pending.secret, normalizeCode(body.Code), a.cfg.Now(), 0)
	if !ok {
		writeError(w, http.StatusBadRequest, "Der Code stimmt nicht. Ist die Uhr des Handys richtig?")
		return
	}
	a.twoFactor[name] = TwoFactor{Secret: pending.secret, Last: step, Recovery: hashes}
	if err := a.save(); err != nil {
		delete(a.twoFactor, name)
		a.saveError(w, err)
		return
	}
	delete(a.pendingSecrets, name)
	slog.Info("two-factor login turned on", "username", name)
	writeJSON(w, http.StatusOK, map[string]any{"recovery": codes})
}

func (a *Auth) disableTwoFactor(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := Username(r)
	if !a.hasSecondFactor(name) {
		writeJSON(w, http.StatusOK, map[string]string{})
		return
	}
	if _, ok := a.checkPassword(name, body.Password); !ok {
		writeError(w, http.StatusForbidden, "Das Passwort stimmt nicht")
		return
	}
	if !a.checkSecondFactor(name, body.Code) {
		writeError(w, http.StatusForbidden, "Der Code stimmt nicht")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.removeTwoFactor(w, name) {
		return
	}
	slog.Info("two-factor login turned off", "username", name)
	writeJSON(w, http.StatusOK, map[string]string{})
}

// removeTwoFactor turns the second factor of name off. It answers the
// request itself and returns false when saving fails. The caller holds mu.
func (a *Auth) removeTwoFactor(w http.ResponseWriter, name string) bool {
	tf, ok := a.twoFactor[name]
	if !ok {
		return true
	}
	delete(a.twoFactor, name)
	if err := a.save(); err != nil {
		a.twoFactor[name] = tf
		a.saveError(w, err)
		return false
	}
	return true
}
