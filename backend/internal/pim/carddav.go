package pim

import (
	"bytes"
	"context"
	"errors"
	"net/http"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"
)

// cardBackend keeps the address books for CardDAV.
type cardBackend struct{ p *PIM }

func (b cardBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	a, err := from(ctx)
	if err != nil {
		return "", err
	}
	return CardPrefix + "/" + a.name + "/", nil
}

func (b cardBackend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	a, err := from(ctx)
	if err != nil {
		return "", err
	}
	return a.home(contacts), nil
}

// addressBook is the CardDAV view of c.
func addressBook(a account, c collection) carddav.AddressBook {
	return carddav.AddressBook{
		Path:            a.home(contacts) + c.ID + "/",
		Name:            c.Name,
		Description:     c.Description,
		MaxResourceSize: maxObject,
		SupportedAddressData: []carddav.AddressDataType{
			{ContentType: vcard.MIMEType, Version: "3.0"},
			{ContentType: vcard.MIMEType, Version: "4.0"},
		},
	}
}

func (b cardBackend) ListAddressBooks(ctx context.Context) ([]carddav.AddressBook, error) {
	a, err := from(ctx)
	if err != nil {
		return nil, err
	}
	list, err := b.p.store.collections(a, contacts)
	if err != nil {
		return nil, davError(err)
	}
	abs := make([]carddav.AddressBook, 0, len(list))
	for _, c := range list {
		abs = append(abs, addressBook(a, c))
	}
	return abs, nil
}

func (b cardBackend) GetAddressBook(ctx context.Context, p string) (*carddav.AddressBook, error) {
	a, err := from(ctx)
	if err != nil {
		return nil, err
	}
	id, obj, err := a.parse(contacts, p)
	if err == nil && (id == "" || obj != "") {
		err = errNotFound
	}
	if err != nil {
		return nil, davError(err)
	}
	c, err := b.p.store.collection(a, contacts, id)
	if err != nil {
		return nil, davError(err)
	}
	ab := addressBook(a, c)
	return &ab, nil
}

func (b cardBackend) CreateAddressBook(ctx context.Context, ab *carddav.AddressBook) error {
	a, err := from(ctx)
	if err != nil {
		return err
	}
	id, obj, err := a.parse(contacts, ab.Path)
	if err == nil && (id == "" || obj != "") {
		err = errForbidden
	}
	if err == nil {
		err = b.p.store.create(a, contacts, collection{ID: id, Name: ab.Name, Description: ab.Description})
	}
	return davError(err)
}

func (b cardBackend) DeleteAddressBook(ctx context.Context, p string) error {
	a, err := from(ctx)
	if err != nil {
		return err
	}
	id, obj, err := a.parse(contacts, p)
	if err == nil && (id == "" || obj != "") {
		err = errForbidden
	}
	if err == nil {
		err = b.p.store.remove(a, contacts, id)
	}
	return davError(err)
}

// addressObject is the CardDAV view of o in the address book id.
func addressObject(a account, id string, o object) (carddav.AddressObject, error) {
	card, err := vcard.NewDecoder(bytes.NewReader(o.data)).Decode()
	if err != nil {
		return carddav.AddressObject{}, err
	}
	return carddav.AddressObject{
		Path:          a.home(contacts) + id + "/" + o.name,
		ModTime:       o.mod,
		ContentLength: int64(len(o.data)),
		ETag:          etag(o.data),
		Card:          card,
	}, nil
}

func (b cardBackend) GetAddressObject(ctx context.Context, p string, _ *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	a, err := from(ctx)
	if err != nil {
		return nil, err
	}
	id, name, err := a.parse(contacts, p)
	if err == nil && name == "" {
		err = errNotFound
	}
	if err != nil {
		return nil, davError(err)
	}
	o, err := b.p.store.object(a, contacts, id, name)
	if err != nil {
		return nil, davError(err)
	}
	ao, err := addressObject(a, id, o)
	if err != nil {
		return nil, davError(err)
	}
	return &ao, nil
}

func (b cardBackend) ListAddressObjects(ctx context.Context, p string, _ *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	a, err := from(ctx)
	if err != nil {
		return nil, err
	}
	id, obj, err := a.parse(contacts, p)
	if err == nil && (id == "" || obj != "") {
		err = errNotFound
	}
	if err != nil {
		return nil, davError(err)
	}
	list, err := b.p.store.objects(a, contacts, id)
	if err != nil {
		return nil, davError(err)
	}
	aos := make([]carddav.AddressObject, 0, len(list))
	for _, o := range list {
		ao, err := addressObject(a, id, o)
		if err != nil {
			// Skips what no client could read rather than failing the whole address book.
			continue
		}
		aos = append(aos, ao)
	}
	return aos, nil
}

func (b cardBackend) QueryAddressObjects(ctx context.Context, p string, query *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	aos, err := b.ListAddressObjects(ctx, p, nil)
	if err != nil {
		return nil, err
	}
	return carddav.Filter(query, aos)
}

func (b cardBackend) PutAddressObject(ctx context.Context, p string, card vcard.Card, opts *carddav.PutAddressObjectOptions) (*carddav.AddressObject, error) {
	a, err := from(ctx)
	if err != nil {
		return nil, err
	}
	id, name, err := a.parse(contacts, p)
	if err == nil && name == "" {
		err = errForbidden
	}
	if err != nil {
		return nil, davError(err)
	}
	if card.Value(vcard.FieldFormattedName) == "" && card.Name() == nil {
		return nil, davError(statusError{http.StatusBadRequest, errors.New("a contact without a name")})
	}
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return nil, davError(statusError{http.StatusBadRequest, err})
	}
	o, err := b.p.store.put(a, contacts, id, name, buf.Bytes(), opts.IfMatch, opts.IfNoneMatch)
	if err != nil {
		return nil, davError(err)
	}
	ao, err := addressObject(a, id, o)
	if err != nil {
		return nil, davError(err)
	}
	return &ao, nil
}

func (b cardBackend) DeleteAddressObject(ctx context.Context, p string) error {
	a, err := from(ctx)
	if err != nil {
		return err
	}
	id, name, err := a.parse(contacts, p)
	if err == nil && name == "" {
		err = errForbidden
	}
	if err == nil {
		err = b.p.store.delete(a, contacts, id, name)
	}
	return davError(err)
}
