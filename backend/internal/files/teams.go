package files

import (
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Team folders: a space of its own that no account owns, made by an admin
// for several accounts, each to read or also to change it. Behind the admin
// check, with a JSON body:
//
//	GET  /api/teams                                        -> {"teams": [{"name", "members": [...]}]}
//	POST /api/teams {"name": "familie"}                    -> 201
//	POST /api/teams/{name}/delete                          -> 200; its folder is moved aside
//	POST /api/teams/{name}/members {"to": "anna", "write": true} -> 200, the member's grant
//	POST /api/teams/{name}/members/delete {"to": "anna"}   -> 200
//
// Each member gets a grant on the whole team folder, with "@<name>" as its
// owner, so it is used like any shared folder (see grants.go). Members who
// may change it can also restore from its recycle bin, since no owner is
// there to do it. A team folder has no quota; it is usersDir/.teams/<name>.
const teamsDir = usersDir + "/.teams"

// TeamRoutes are the API paths of team folders behind the admin check.
var TeamRoutes = []string{
	"GET /api/teams",
	"POST /api/teams",
	"POST /api/teams/{name}/delete",
	"POST /api/teams/{name}/members",
	"POST /api/teams/{name}/members/delete",
}

// teamWriteRoutes are what members who may change a team folder can also do.
var teamWriteRoutes = []string{
	"GET /api/trash",
	"POST /api/trash/restore",
}

// Team is a team folder with its members.
type Team struct {
	Name    string  `json:"name"`
	Members []Grant `json:"members"`
}

// teamOwner is the owner of the grants of the team folder name.
func teamOwner(name string) string { return "@" + name }

// RegisterTeams adds TeamRoutes to mux behind requireAdmin.
func (s *Spaces) RegisterTeams(mux *http.ServeMux, requireAdmin func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/teams", requireAdmin(s.listTeams))
	mux.HandleFunc("POST /api/teams", requireAdmin(s.createTeam))
	mux.HandleFunc("POST /api/teams/{name}/delete", requireAdmin(s.deleteTeam))
	mux.HandleFunc("POST /api/teams/{name}/members", requireAdmin(s.setMember))
	mux.HandleFunc("POST /api/teams/{name}/members/delete", requireAdmin(s.removeMember))
}

// teamNames are the team folders there are, by name.
func (s *Spaces) teamNames() []string {
	names := []string{}
	entries, err := fs.ReadDir(s.owner.root.FS(), teamsDir)
	if err != nil {
		return names
	}
	for _, e := range entries {
		if e.IsDir() && accountName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names
}

// team checks the name of the request's team folder. It answers the request
// itself and returns false when there is no such team folder.
func (s *Spaces) team(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if !slices.Contains(s.teamNames(), name) {
		writeError(w, http.StatusNotFound, "Team-Ordner nicht gefunden")
		return "", false
	}
	return name, true
}

func (s *Spaces) listTeams(w http.ResponseWriter, _ *http.Request) {
	s.grantsMu.Lock()
	all := s.readGrants()
	s.grantsMu.Unlock()
	teams := []Team{}
	for _, name := range s.teamNames() {
		t := Team{Name: name, Members: []Grant{}}
		for _, g := range all {
			if g.Owner == teamOwner(name) {
				t.Members = append(t.Members, g)
			}
		}
		slices.SortFunc(t.Members, func(a, b Grant) int { return strings.Compare(a.To, b.To) })
		teams = append(teams, t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": teams})
}

func (s *Spaces) createTeam(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !accountName.MatchString(body.Name) {
		writeError(w, http.StatusBadRequest, "Name: Kleinbuchstaben, Ziffern, Punkt, Binde- und Unterstrich, höchstens 32 Zeichen")
		return
	}
	if slices.Contains(s.teamNames(), body.Name) {
		writeError(w, http.StatusConflict, "Existiert bereits")
		return
	}
	if err := s.owner.root.MkdirAll(spaceDir(teamOwner(body.Name)), 0o750); err != nil {
		s.owner.diskError(w, "creating a team folder", err)
		return
	}
	slog.Info("team folder created", "team", body.Name)
	s.owner.noteBy(r, s.cfg.User(r), "team-create", body.Name)
	writeJSON(w, http.StatusCreated, Team{Name: body.Name, Members: []Grant{}})
}

func (s *Spaces) deleteTeam(w http.ResponseWriter, r *http.Request) {
	name, ok := s.team(w, r)
	if !ok {
		return
	}
	s.retireGrants(teamOwner(name))
	if err := s.moveAside(teamOwner(name), ".team-"+name); err != nil {
		s.owner.diskError(w, "deleting a team folder", err)
		return
	}
	s.owner.noteBy(r, s.cfg.User(r), "team-delete", name)
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

func (s *Spaces) setMember(w http.ResponseWriter, r *http.Request) {
	name, ok := s.team(w, r)
	if !ok {
		return
	}
	var body struct {
		To    string `json:"to"`
		Write bool   `json:"write"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if s.cfg.Accounts == nil || !slices.Contains(s.cfg.Accounts(), body.To) {
		writeError(w, http.StatusBadRequest, "Unbekanntes Konto")
		return
	}
	id, err := newID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Hinzufügen fehlgeschlagen")
		return
	}
	g := Grant{ID: id, Owner: teamOwner(name), Path: "/", Name: name, To: body.To, Write: body.Write, Team: true, Created: time.Now().UTC()}
	isNew := true
	err = s.changeGrants(func(list []Grant) []Grant {
		for i, old := range list {
			if old.Owner == g.Owner && old.To == g.To {
				list[i].Write = g.Write
				g, isNew = list[i], false
				return list
			}
		}
		return append(list, g)
	})
	if err != nil {
		s.owner.diskError(w, "adding a team member", err)
		return
	}
	slog.Info("team member set", "team", name, "member", g.To, "write", g.Write)
	s.owner.noteBy(r, s.cfg.User(r), "team-member", name+" → "+g.To+" ("+rights(g.Write)+")")
	if isNew && s.cfg.Shared != nil {
		s.cfg.Shared(g)
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Spaces) removeMember(w http.ResponseWriter, r *http.Request) {
	name, ok := s.team(w, r)
	if !ok {
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	found := false
	err := s.changeGrants(func(list []Grant) []Grant {
		return slices.DeleteFunc(list, func(g Grant) bool {
			if g.Owner == teamOwner(name) && g.To == body.To {
				found = true
				return true
			}
			return false
		})
	})
	if err != nil {
		s.owner.diskError(w, "removing a team member", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "Kein Mitglied")
		return
	}
	s.owner.noteBy(r, s.cfg.User(r), "team-member-delete", name+" → "+body.To)
	writeJSON(w, http.StatusOK, map[string]string{"to": body.To})
}
