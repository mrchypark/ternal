package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	ui "github.com/mrchypark/ely-htmx"
	"github.com/mrchypark/ternal/internal/auth"
	"github.com/mrchypark/ternal/internal/core"
	"github.com/mrchypark/ternal/internal/store"
)

//go:embed static/* templates/*.html
var assets embed.FS

var templates = template.Must(template.Must(ui.NewTemplates()).Funcs(template.FuncMap{"role": role}).ParseFS(assets, "templates/*.html"))

type Server struct{ store *store.Store }

func New(s *store.Store) *Server { return &Server{store: s} }

func Assets() http.Handler {
	root, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	if err := ui.BindAssets(mux); err != nil {
		panic(err)
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(root))))
	return mux
}

type pageData struct {
	Identity                 *auth.AuthContext
	View, Title, Description string
	Tables                   []tableData
	Navigation               []navItem
	OutOfBand                bool
}
type navItem struct{ View, Label string }
type tableData struct {
	ID, Caption, EmptyLabel string
	Columns                 []column
	Rows                    []row
}
type column struct{ Label string }
type row struct{ Cells []string }

func (s *Server) Index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	data := pageData{Identity: auth.GetAuth(r), View: r.URL.Query().Get("view")}
	if data.View == "" {
		data.View = "hosts"
	}
	name := "landing"
	if data.Identity != nil {
		if !data.Identity.IsAdmin && (data.View == "policies" || data.View == "audit") {
			http.Error(w, "Administrator access required.", http.StatusForbidden)
			return
		}
		var err error
		data.Tables, data.Title, data.Description, err = s.view(r, data.Identity, data.View)
		if err != nil {
			http.Error(w, "Unable to load the workspace.", http.StatusInternalServerError)
			return
		}
		for _, item := range []navItem{{"hosts", "Hosts"}, {"keys", "SSH keys"}, {"policies", "Policies"}, {"access", "Access"}, {"audit", "Audit"}} {
			if data.Identity.IsAdmin || (item.View != "policies" && item.View != "audit") {
				data.Navigation = append(data.Navigation, item)
			}
		}
		name = "page"
		if r.Header.Get("HX-Request") == "true" {
			name = "fragment"
			data.OutOfBand = true
		}
	}
	// Buffer rendering so template failures cannot send a partial successful page.
	var output bytes.Buffer
	if err := templates.ExecuteTemplate(&output, name, data); err != nil {
		http.Error(w, "Unable to render the page.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(output.Bytes())
}

func (s *Server) view(r *http.Request, identity *auth.AuthContext, view string) ([]tableData, string, string, error) {
	switch view {
	case "hosts":
		items, err := s.store.ListHosts(r.Context())
		if err == nil && !identity.IsAdmin {
			policies, policyErr := s.store.ListPolicies(r.Context())
			if policyErr != nil {
				return nil, "", "", policyErr
			}
			items = core.FilterVisibleHosts(&core.UserClaims{Subject: identity.User.PrincipalID(), Groups: identity.User.Groups, CustomClaims: identity.User.CustomClaims}, items, policies)
		}
		return []tableData{hostsTable(items)}, "Host inventory", "Registered endpoints and just-in-time SSH access.", err
	case "keys":
		identity := auth.GetAuth(r)
		items, err := s.store.ListSSHKeys(r.Context(), identity.User.PrincipalID())
		return []tableData{keysTable(items)}, "SSH keys", "User credentials authorized for access grants.", err
	case "policies":
		if !identity.IsAdmin {
			return nil, "", "", fmt.Errorf("administrator access required")
		}
		items, err := s.store.ListPolicies(r.Context())
		return []tableData{policiesTable(items)}, "Access policies", "Organization principals mapped to host selectors.", err
	case "access":
		var grants []store.AccessGrant
		var requests []store.AccessRequest
		var err error
		if identity.IsAdmin {
			grants, err = s.store.ListAccessGrants(r.Context())
			if err != nil {
				return nil, "", "", err
			}
			requests, err = s.store.ListAccessRequests(r.Context())
		} else {
			grants, err = s.store.ListUserAccessGrants(r.Context(), identity.User.PrincipalID())
			if err != nil {
				return nil, "", "", err
			}
			requests, err = s.store.ListUserAccessRequests(r.Context(), identity.User.PrincipalID())
		}
		return accessTables(grants, requests), "Access control", "Short-lived grants and their policy decisions.", err
	case "audit":
		if !identity.IsAdmin {
			return nil, "", "", fmt.Errorf("administrator access required")
		}
		items, err := s.store.ListAuditEvents(r.Context(), 100)
		return []tableData{auditTable(items)}, "Audit log", "Access and administration activity retained for review.", err
	default:
		return nil, "", "", fmt.Errorf("unknown view %q", view)
	}
}

func newTable(id, caption, empty string, headers ...string) tableData {
	table := tableData{ID: id, Caption: caption, EmptyLabel: empty}
	for _, label := range headers {
		table.Columns = append(table.Columns, column{label})
	}
	return table
}

func hostsTable(items []core.Host) tableData {
	table := newTable("hosts-table", "Registered SSH hosts", "No hosts registered yet.", "Name", "Status", "User", "Port", "Endpoint", "Last seen")
	for _, item := range items {
		table.Rows = append(table.Rows, row{[]string{item.Name, status(item.Status), item.SSHUser, strconv.Itoa(int(item.SSHPort)), short(item.EndpointID), formatUnix(item.LastSeen)}})
	}
	return table
}
func keysTable(items []store.SshKey) tableData {
	table := newTable("keys-table", "Registered SSH public keys", "No SSH keys registered yet.", "Fingerprint", "Public key", "Created")
	for _, item := range items {
		table.Rows = append(table.Rows, row{[]string{short(item.Fingerprint), short(item.PublicKey), formatTime(item.CreatedAt)}})
	}
	return table
}
func policiesTable(items []core.Policy) tableData {
	table := newTable("policies-table", "Organization access policies", "No policies configured yet.", "Name", "Principal", "Host selector", "SSH users", "Expires")
	for _, item := range items {
		table.Rows = append(table.Rows, row{[]string{item.Name, item.Principal, item.HostSelector, strings.Join(item.SSHUsers, ", "), formatUnix(item.ExpiresAt)}})
	}
	return table
}
func accessTables(grants []store.AccessGrant, requests []store.AccessRequest) []tableData {
	issued := newTable("grants-table", "Issued access grants", "No active grants.", "User", "Host", "SSH user", "Expires")
	for _, item := range grants {
		issued.Rows = append(issued.Rows, row{[]string{item.UserID, item.HostID, item.SSHUser, formatTime(item.ExpiresAt)}})
	}
	decisions := newTable("requests-table", "Access request decisions", "No access requests recorded.", "Requester", "Host", "SSH user", "Status", "Created")
	for _, item := range requests {
		decisions.Rows = append(decisions.Rows, row{[]string{item.UserID, item.HostID, item.SSHUser, status(item.Status), formatTime(item.CreatedAt)}})
	}
	return []tableData{issued, decisions}
}
func auditTable(items []store.AuditEvent) tableData {
	table := newTable("audit-table", "Audit events", "No audit events recorded.", "Time", "Actor", "Action", "Target")
	for _, item := range items {
		table.Rows = append(table.Rows, row{[]string{formatTime(item.CreatedAt), item.UserID, item.Action, item.Resource + " / " + item.ResourceID}})
	}
	return table
}

func status(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func short(value string) string {
	if len(value) <= 30 {
		return value
	}
	return value[:14] + "…" + value[len(value)-10:]
}

func formatUnix(value *int64) string {
	if value == nil {
		return "—"
	}
	return formatTime(*value)
}

func formatTime(value int64) string {
	if value <= 0 {
		return "—"
	}
	return time.Unix(value, 0).UTC().Format("2006-01-02 15:04 UTC")
}

func role(identity *auth.AuthContext) string {
	if identity.IsAdmin {
		return "Administrator"
	}
	return "User"
}
