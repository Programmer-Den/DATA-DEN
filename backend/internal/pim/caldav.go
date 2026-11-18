package pim

import (
	"bytes"
	"context"
	"errors"
	"net/http"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

// calBackend keeps the calendars for CalDAV.
type calBackend struct{ p *PIM }

func (b calBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	a, err := from(ctx)
	if err != nil {
		return "", err
	}
	return CalPrefix + "/" + a.name + "/", nil
}

func (b calBackend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	a, err := from(ctx)
	if err != nil {
		return "", err
	}
	return a.home(calendars), nil
}

// calendar is the CalDAV view of c.
func calendar(a account, c collection) caldav.Calendar {
	return caldav.Calendar{
		Path:                  a.home(calendars) + c.ID + "/",
		Name:                  c.Name,
		Description:           c.Description,
		MaxResourceSize:       maxObject,
		SupportedComponentSet: []string{ical.CompEvent, ical.CompToDo, ical.CompJournal},
	}
}

func (b calBackend) CreateCalendar(ctx context.Context, cal *caldav.Calendar) error {
	a, err := from(ctx)
	if err != nil {
		return err
	}
	id, obj, err := a.parse(calendars, cal.Path)
	if err == nil && (id == "" || obj != "") {
		err = errForbidden
	}
	if err == nil {
		err = b.p.store.create(a, calendars, collection{ID: id, Name: cal.Name, Description: cal.Description})
	}
	return davError(err)
}

func (b calBackend) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	a, err := from(ctx)
	if err != nil {
		return nil, err
	}
	list, err := b.p.store.collections(a, calendars)
	if err != nil {
		return nil, davError(err)
	}
	cals := make([]caldav.Calendar, 0, len(list))
	for _, c := range list {
		cals = append(cals, calendar(a, c))
	}
	return cals, nil
}

func (b calBackend) GetCalendar(ctx context.Context, p string) (*caldav.Calendar, error) {
	a, err := from(ctx)
	if err != nil {
		return nil, err
	}
	id, obj, err := a.parse(calendars, p)
	if err == nil && (id == "" || obj != "") {
		err = errNotFound
	}
	if err != nil {
		return nil, davError(err)
	}
	c, err := b.p.store.collection(a, calendars, id)
	if err != nil {
		return nil, davError(err)
	}
	cal := calendar(a, c)
	return &cal, nil
}

// calendarObject is the CalDAV view of o in the calendar id.
func calendarObject(a account, id string, o object) (caldav.CalendarObject, error) {
	data, err := ical.NewDecoder(bytes.NewReader(o.data)).Decode()
	if err != nil {
		return caldav.CalendarObject{}, err
	}
	return caldav.CalendarObject{
		Path:          a.home(calendars) + id + "/" + o.name,
		ModTime:       o.mod,
		ContentLength: int64(len(o.data)),
		ETag:          etag(o.data),
		Data:          data,
	}, nil
}

func (b calBackend) GetCalendarObject(ctx context.Context, p string, _ *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	a, err := from(ctx)
	if err != nil {
		return nil, err
	}
	id, name, err := a.parse(calendars, p)
	if err == nil && name == "" {
		err = errNotFound
	}
	if err != nil {
		return nil, davError(err)
	}
	o, err := b.p.store.object(a, calendars, id, name)
	if err != nil {
		return nil, davError(err)
	}
	co, err := calendarObject(a, id, o)
	if err != nil {
		return nil, davError(err)
	}
	return &co, nil
}

func (b calBackend) ListCalendarObjects(ctx context.Context, p string, _ *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	a, err := from(ctx)
	if err != nil {
		return nil, err
	}
	id, obj, err := a.parse(calendars, p)
	if err == nil && (id == "" || obj != "") {
		err = errNotFound
	}
	if err != nil {
		return nil, davError(err)
	}
	list, err := b.p.store.objects(a, calendars, id)
	if err != nil {
		return nil, davError(err)
	}
	cos := make([]caldav.CalendarObject, 0, len(list))
	for _, o := range list {
		co, err := calendarObject(a, id, o)
		if err != nil {
			// Skips what no client could read rather than failing the whole calendar.
			continue
		}
		cos = append(cos, co)
	}
	return cos, nil
}

func (b calBackend) QueryCalendarObjects(ctx context.Context, p string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	cos, err := b.ListCalendarObjects(ctx, p, nil)
	if err != nil {
		return nil, err
	}
	return caldav.Filter(query, cos)
}

// checkCalendar refuses data that is no event, task or journal entry, or
// one without a UID.
func checkCalendar(cal *ical.Calendar) error {
	found := false
	for _, c := range cal.Children {
		switch c.Name {
		case ical.CompEvent, ical.CompToDo, ical.CompJournal:
			if uid := c.Props.Get(ical.PropUID); uid == nil || uid.Value == "" {
				return errors.New("a component without UID")
			}
			found = true
		}
	}
	if !found {
		return errors.New("no event, task or journal entry")
	}
	return nil
}

func (b calBackend) PutCalendarObject(ctx context.Context, p string, cal *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	a, err := from(ctx)
	if err != nil {
		return nil, err
	}
	id, name, err := a.parse(calendars, p)
	if err == nil && name == "" {
		err = errForbidden
	}
	if err != nil {
		return nil, davError(err)
	}
	if err := checkCalendar(cal); err != nil {
		return nil, caldav.NewPreconditionError(caldav.PreconditionValidCalendarObjectResource)
	}
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, davError(statusError{http.StatusBadRequest, err})
	}
	o, err := b.p.store.put(a, calendars, id, name, buf.Bytes(), opts.IfMatch, opts.IfNoneMatch)
	if err != nil {
		return nil, davError(err)
	}
	co, err := calendarObject(a, id, o)
	if err != nil {
		return nil, davError(err)
	}
	return &co, nil
}

// DeleteCalendarObject deletes an event, or a whole calendar for the path
// of one.
func (b calBackend) DeleteCalendarObject(ctx context.Context, p string) error {
	a, err := from(ctx)
	if err != nil {
		return err
	}
	id, name, err := a.parse(calendars, p)
	switch {
	case err != nil:
	case id == "":
		err = errForbidden
	case name == "":
		err = b.p.store.remove(a, calendars, id)
	default:
		err = b.p.store.delete(a, calendars, id, name)
	}
	return davError(err)
}
