package web

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"

	"github.com/mrchypark/ternal/internal/auth"
	"github.com/mrchypark/ternal/internal/core"
	"github.com/mrchypark/ternal/internal/store"
)

//go:embed static/*
var assets embed.FS

type Server struct {
	store *store.Store
}

func New(s *store.Store) *Server { return &Server{store: s} }

func Assets() http.Handler {
	root, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/assets/", http.FileServer(http.FS(root)))
}

func (s *Server) Index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	identity := auth.GetAuth(r)
	view := r.URL.Query().Get("view")
	if view == "" {
		view = "hosts"
	}

	var node g.Node
	if identity == nil {
		node = landing()
	} else {
		if !identity.IsAdmin && (view == "policies" || view == "audit") {
			http.Error(w, "Administrator access required.", http.StatusForbidden)
			return
		}
		workspace, err := s.workspace(r, identity, view)
		if err != nil {
			http.Error(w, "Unable to load the workspace.", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			node = g.Group{workspace, navigation(identity, view, true)}
		} else {
			node = page(identity, view, workspace)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := node.Render(w); err != nil {
		http.Error(w, "Unable to render the page.", http.StatusInternalServerError)
	}
}

func landing() g.Node {
	return document("Ternal · SSH for device management",
		h.Body(
			h.Class("min-h-screen bg-ink-900 text-ink-100"),
			h.Main(
				h.Class("mx-auto grid min-h-screen max-w-7xl content-between gap-20 px-6 py-8 sm:px-10 lg:px-16"),
				h.Header(h.Class("flex items-center justify-between border-b border-ink-700 pb-5"), brand(), h.Span(h.Class("text-xs font-semibold tracking-[0.2em] text-ink-400"), g.Text("CONTROL PLANE"))),
				h.Section(
					h.Class("grid items-end gap-12 lg:grid-cols-[1.4fr_0.6fr]"),
					h.Div(
						h.P(h.Class("mb-5 text-xs font-bold tracking-[0.22em] text-forest-100"), g.Text("ZERO STANDING CREDENTIALS")),
						h.H1(h.Class("max-w-4xl text-5xl font-semibold leading-[0.98] tracking-[-0.045em] text-ink-50 sm:text-7xl"), g.Text("Reach managed devices without leaving permanent SSH access behind.")),
					),
					h.Div(
						h.Class("border-l border-ink-700 pl-6"),
						h.P(h.Class("mb-7 text-base leading-7 text-ink-200"), g.Text("Short-lived grants, explicit policy, and strict host identity verification for every session.")),
						h.A(h.Href("/auth/login"), h.Class("inline-flex min-h-12 items-center bg-forest-500 px-6 text-sm font-bold text-ink-50 transition-colors hover:bg-forest-600"), g.Text("Sign in with your organization")),
					),
				),
				h.Footer(h.Class("grid gap-4 border-t border-ink-700 pt-5 text-xs text-ink-400 sm:grid-cols-3"),
					h.Span(g.Text("01 · Identity verified")),
					h.Span(g.Text("02 · Policy evaluated")),
					h.Span(g.Text("03 · Access expires")),
				),
			),
		),
	)
}

func page(identity *auth.AuthContext, active string, workspace g.Node) g.Node {
	return document("Ternal · Control plane",
		h.Body(
			h.Class("min-h-screen bg-ink-50"),
			h.A(h.Href("#workspace"), h.Class("sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-50 focus:bg-ink-900 focus:px-4 focus:py-3 focus:text-ink-50"), g.Text("Skip to workspace")),
			h.Div(h.Class("min-h-screen lg:grid lg:grid-cols-[17rem_1fr]"),
				h.Aside(h.Class("border-b border-ink-200 bg-ink-900 px-5 py-6 text-ink-100 lg:fixed lg:inset-y-0 lg:w-[17rem] lg:border-b-0 lg:border-r lg:border-ink-700"),
					brand(),
					navigation(identity, active, false),
					h.Div(h.Class("mt-8 hidden border-t border-ink-700 pt-5 text-sm lg:block"),
						h.P(h.Class("font-semibold text-ink-100"), g.Text(identity.User.Subject)),
						h.P(h.Class("mt-1 text-xs text-ink-400"), g.Text(role(identity))),
						h.Form(h.Method("post"), h.Action("/auth/logout"), h.Class("mt-4"),
							h.Input(h.Type("hidden"), h.Name("_csrf"), h.Value(identity.CSRFToken)),
							h.Button(h.Type("submit"), h.Class("min-h-11 text-sm font-semibold text-ink-200 underline-offset-4 hover:text-ink-50 hover:underline"), g.Text("Sign out")),
						),
					),
				),
				h.Main(h.Class("lg:col-start-2"), workspace),
			),
		),
	)
}

func (s *Server) workspace(r *http.Request, identity *auth.AuthContext, view string) (g.Node, error) {
	content, title, description, err := s.view(r, identity, view)
	if err != nil {
		return nil, err
	}
	return h.Section(h.ID("workspace"), h.TabIndex("-1"), h.Class("mx-auto max-w-[94rem] px-5 py-8 sm:px-8 lg:px-12 lg:py-12"),
		h.Header(h.Class("mb-10 grid gap-4 border-b border-ink-200 pb-7 sm:grid-cols-[1fr_auto] sm:items-end"),
			h.Div(
				h.P(h.Class("mb-2 text-xs font-bold tracking-[0.18em] text-forest-600"), g.Text("CONTROL PLANE / "+strings.ToUpper(view))),
				h.H1(h.Class("text-4xl font-semibold tracking-[-0.04em] text-ink-900 sm:text-5xl"), g.Text(title)),
				h.P(h.Class("mt-3 max-w-2xl text-base leading-7 text-ink-500"), g.Text(description)),
			),
			h.Button(h.Type("button"), h.Class("min-h-11 border border-ink-200 bg-white px-4 text-sm font-semibold text-ink-700 hover:border-ink-400"), hx("get", "/?view="+view), hx("target", "#workspace"), hx("swap", "outerHTML"), g.Text("Refresh data")),
		),
		content,
	), nil
}

func (s *Server) view(r *http.Request, identity *auth.AuthContext, view string) (g.Node, string, string, error) {
	switch view {
	case "hosts":
		items, err := s.store.ListHosts(r.Context())
		if err == nil && !identity.IsAdmin {
			policies, policyErr := s.store.ListPolicies(r.Context())
			if policyErr != nil {
				return nil, "", "", policyErr
			}
			items = core.FilterVisibleHosts(&core.UserClaims{Subject: identity.User.Subject, Groups: identity.User.Groups, CustomClaims: identity.User.CustomClaims}, items, policies)
		}
		return hostsTable(items), "Host inventory", "Registered endpoints and just-in-time SSH access.", err
	case "keys":
		identity := auth.GetAuth(r)
		items, err := s.store.ListSSHKeys(r.Context(), identity.User.Subject)
		return keysTable(items), "SSH keys", "User credentials authorized for access grants.", err
	case "policies":
		if !identity.IsAdmin {
			return nil, "", "", fmt.Errorf("administrator access required")
		}
		items, err := s.store.ListPolicies(r.Context())
		return policiesTable(items), "Access policies", "Organization principals mapped to host selectors.", err
	case "access":
		grants, err := s.store.ListAccessGrants(r.Context())
		if err != nil {
			return nil, "", "", err
		}
		requests, err := s.store.ListAccessRequests(r.Context())
		if !identity.IsAdmin {
			grants = filterGrants(grants, identity.User.Subject)
			requests = filterRequests(requests, identity.User.Subject)
		}
		return accessTables(grants, requests), "Access control", "Short-lived grants and their policy decisions.", err
	case "audit":
		if !identity.IsAdmin {
			return nil, "", "", fmt.Errorf("administrator access required")
		}
		items, err := s.store.ListAuditEvents(r.Context(), 100)
		return auditTable(items), "Audit log", "Access and administration activity retained for review.", err
	default:
		return nil, "", "", fmt.Errorf("unknown view %q", view)
	}
}

func document(title string, body g.Node) g.Node {
	return h.Doctype(h.HTML(h.Lang("en"),
		h.Head(
			h.Meta(h.Charset("utf-8")),
			h.Meta(h.Name("viewport"), h.Content("width=device-width, initial-scale=1, viewport-fit=cover")),
			h.Meta(h.Name("description"), h.Content("Secure SSH access for managed devices")),
			h.Meta(h.Name("htmx-config"), h.Content(`{"noSwap":[204,304,"4xx","5xx"]}`)),
			h.TitleEl(g.Text(title)),
			h.Link(h.Rel("stylesheet"), h.Href("/assets/app.css")),
			h.Script(h.Src("/assets/htmx.min.js"), h.Defer()),
		),
		body,
	))
}

func brand() g.Node {
	return h.A(h.Href("/"), h.Class("inline-flex items-baseline gap-2 text-ink-50 no-underline"),
		h.Span(h.Class("text-2xl font-semibold tracking-[-0.045em]"), g.Text("Ternal")),
		h.Span(h.Class("text-[0.65rem] font-bold tracking-[0.18em] text-forest-100"), g.Text("SSH")),
	)
}

func navItems(identity *auth.AuthContext, active string) []g.Node {
	items := [][2]string{{"hosts", "Hosts"}, {"keys", "SSH keys"}, {"policies", "Policies"}, {"access", "Access"}, {"audit", "Audit"}}
	if !identity.IsAdmin {
		items = [][2]string{{"hosts", "Hosts"}, {"keys", "SSH keys"}, {"access", "Access"}}
	}
	return g.Map(items, func(item [2]string) g.Node {
		class := "min-h-11 px-3 py-3 text-sm font-semibold text-ink-400 transition-colors hover:bg-ink-700 hover:text-ink-50"
		if item[0] == active {
			class = "min-h-11 bg-ink-700 px-3 py-3 text-sm font-semibold text-ink-50"
		}
		url := "/?view=" + item[0]
		attrs := []g.Node{h.Href(url), h.Class(class), hx("get", url), hx("target", "#workspace"), hx("swap", "outerHTML"), hx("push-url", "true")}
		if item[0] == active {
			attrs = append(attrs, h.Aria("current", "page"))
		}
		return h.A(append(attrs, g.Text(item[1]))...)
	})
}

func navigation(identity *auth.AuthContext, active string, outOfBand bool) g.Node {
	attrs := []g.Node{h.ID("workspace-navigation"), h.Aria("label", "Workspace"), h.Class("mt-8 grid grid-cols-2 gap-1 sm:grid-cols-5 lg:grid-cols-1")}
	if outOfBand {
		attrs = append(attrs, hx("swap-oob", "outerHTML"))
	}
	return h.Nav(append(attrs, g.Group(navItems(identity, active)))...)
}

func filterGrants(items []store.AccessGrant, userID string) []store.AccessGrant {
	filtered := make([]store.AccessGrant, 0, len(items))
	for _, item := range items {
		if item.UserID == userID {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterRequests(items []store.AccessRequest, userID string) []store.AccessRequest {
	filtered := make([]store.AccessRequest, 0, len(items))
	for _, item := range items {
		if item.UserID == userID {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func hostsTable(items []core.Host) g.Node {
	rows := g.Map(items, func(item core.Host) g.Node {
		return tr(
			td(item.Name, "font-semibold text-ink-900"),
			td(status(item.Status), ""),
			td(item.SSHUser, ""),
			td(strconv.Itoa(int(item.SSHPort)), "tabular-nums"),
			td(short(item.EndpointID), "font-mono text-xs"),
			td(formatUnix(item.LastSeen), "tabular-nums"),
		)
	})
	return dataTable("Registered SSH hosts", []string{"Name", "Status", "User", "Port", "Endpoint", "Last seen"}, rows, "No hosts registered yet.")
}

func keysTable(items []store.SshKey) g.Node {
	rows := g.Map(items, func(item store.SshKey) g.Node {
		return tr(td(short(item.Fingerprint), "font-mono text-xs"), td(short(item.PublicKey), "font-mono text-xs"), td(formatTime(item.CreatedAt), "tabular-nums"))
	})
	return dataTable("Registered SSH public keys", []string{"Fingerprint", "Public key", "Created"}, rows, "No SSH keys registered yet.")
}

func policiesTable(items []core.Policy) g.Node {
	rows := g.Map(items, func(item core.Policy) g.Node {
		return tr(td(item.Name, "font-semibold text-ink-900"), td(item.Principal, ""), td(item.HostSelector, "font-mono text-xs"), td(strings.Join(item.SSHUsers, ", "), ""), td(formatUnix(item.ExpiresAt), "tabular-nums"))
	})
	return dataTable("Organization access policies", []string{"Name", "Principal", "Host selector", "SSH users", "Expires"}, rows, "No policies configured yet.")
}

func accessTables(grants []store.AccessGrant, requests []store.AccessRequest) g.Node {
	grantRows := g.Map(grants, func(item store.AccessGrant) g.Node {
		return tr(td(item.UserID, ""), td(item.HostID, "font-mono text-xs"), td(item.SSHUser, ""), td(formatTime(item.ExpiresAt), "tabular-nums"))
	})
	requestRows := g.Map(requests, func(item store.AccessRequest) g.Node {
		return tr(td(item.UserID, ""), td(item.HostID, "font-mono text-xs"), td(item.SSHUser, ""), td(status(item.Status), ""), td(formatTime(item.CreatedAt), "tabular-nums"))
	})
	return h.Div(h.Class("grid gap-10"),
		dataTable("Issued access grants", []string{"User", "Host", "SSH user", "Expires"}, grantRows, "No active grants."),
		dataTable("Access request decisions", []string{"Requester", "Host", "SSH user", "Status", "Created"}, requestRows, "No access requests recorded."),
	)
}

func auditTable(items []store.AuditEvent) g.Node {
	rows := g.Map(items, func(item store.AuditEvent) g.Node {
		return tr(td(formatTime(item.CreatedAt), "tabular-nums"), td(item.UserID, ""), td(item.Action, "font-semibold text-ink-900"), td(item.Resource+" / "+item.ResourceID, "font-mono text-xs"))
	})
	return dataTable("Audit events", []string{"Time", "Actor", "Action", "Target"}, rows, "No audit events recorded.")
}

func dataTable(caption string, headers []string, rows g.Group, empty string) g.Node {
	body := g.Node(rows)
	if len(rows) == 0 {
		body = h.Tr(h.Td(h.ColSpan(strconv.Itoa(len(headers))), h.Class("px-5 py-12 text-center text-sm text-ink-500"), g.Text(empty)))
	}
	return h.Section(h.Class("overflow-hidden border border-ink-200 bg-white"),
		h.Div(h.Class("flex items-center justify-between border-b border-ink-200 px-5 py-4"), h.H2(h.Class("text-lg font-semibold text-ink-900"), g.Text(caption)), h.Span(h.Class("text-xs font-bold tracking-[0.12em] text-ink-400"), g.Text(strconv.Itoa(len(rows))+" TOTAL"))),
		h.Div(h.Class("overflow-x-auto"),
			h.Table(h.Aria("label", caption), h.Class("w-full min-w-[44rem] border-collapse text-left text-sm"),
				h.THead(h.Class("bg-ink-100 text-xs uppercase tracking-[0.1em] text-ink-500"), h.Tr(g.Map(headers, func(label string) g.Node { return h.Th(h.Scope("col"), h.Class("px-5 py-3 font-bold"), g.Text(label)) })...)),
				h.TBody(h.Class("divide-y divide-ink-200"), body),
			),
		),
	)
}

func tr(cells ...g.Node) g.Node {
	return h.Tr(h.Class("transition-colors hover:bg-forest-50"), g.Group(cells))
}
func td(text, class string) g.Node { return h.Td(h.Class("px-5 py-4 align-top "+class), g.Text(text)) }
func hx(name, value string) g.Node { return g.Attr("hx-"+name, value) }

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
