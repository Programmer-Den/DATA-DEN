package auth

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"
)

// ownerFile is the owner's account as the setup page stores it, for
// installations without DATA_DEN_PASSWORD_HASH.
type ownerFile struct {
	Name         string `json:"name"`
	PasswordHash string `json:"passwordHash"`
}

// ValidName reports whether name may be the name of a new account.
func ValidName(name string) bool {
	return accountName.MatchString(name)
}

// LoadOwner reads the owner's account from file. Both are empty when there
// is no file yet, before the setup.
func LoadOwner(file string) (name, passwordHash string, err error) {
	data, err := os.ReadFile(filepath.Clean(file))
	if errors.Is(err, fs.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	var owner ownerFile
	if err := json.Unmarshal(data, &owner); err != nil {
		return "", "", errors.New(file + " is not valid JSON: " + err.Error())
	}
	if _, err := bcrypt.Cost([]byte(owner.PasswordHash)); !ValidName(owner.Name) || err != nil {
		return "", "", errors.New(file + " holds no valid account; delete it to set the owner up again")
	}
	return owner.Name, owner.PasswordHash, nil
}

// SaveOwner writes the owner's account to file, in one step.
func SaveOwner(file, name, passwordHash string) error {
	data, err := json.MarshalIndent(ownerFile{Name: name, PasswordHash: passwordHash}, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(file, data)
}
