package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ui "github.com/mrchypark/ely-htmx"

	"github.com/mrchypark/ternal/internal/auth"
	"github.com/mrchypark/ternal/internal/store"
)

func TestPortalRendersHTMXAndEscapesData(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.CreateHost(context.Background(), store.NewHost{
		Name:       "escape-test",
		EndpointID: strings.Repeat("a", 64),
		SSHUser:    "ops",
		SSHPort:    22,
		Status:     `<script>alert("x")</script>`,
	}, "system"); err != nil {
		t.Fatal(err)
	}

	handler := auth.AuthMiddleware(strings.Repeat("s", 32), true, "ternal-admins")(http.HandlerFunc(New(s).Index))
	req := httptest.NewRequest(http.MethodGet, "/?view=hosts", nil)
	req.Header.Set("X-Ternal-User", "operator@example.com")
	req.Header.Set("X-Ternal-Groups", "ternal-admins")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, expected := range []string{
		`src="/assets/htmx.min.js"`,
		`rel="icon" type="image/png" sizes="214x256" href="/assets/ternal-icon-256.png"`,
		`aria-label="Ternal home"`,
		`src="/assets/ternal-logo-640.png" alt="Ternal logo" width="640" height="251"`,
		`hx-get="/?view=policies"`,
		`hx-target="#workspace"`,
		`id="workspace" tabindex="-1"`,
		`aria-current="page"`,
		`<caption>Registered SSH hosts</caption>`,
		`name="htmx-config" content='{"noSwap":[204,304,"4xx","5xx"]}'`,
		`data-slot="button"`,
		`class="ui-table"`,
		`name="_csrf" value="dev-csrf"`,
		`&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("response missing %q", expected)
		}
	}
	if strings.Contains(body, `<script>alert("x")</script>`) {
		t.Fatal("stored host name was rendered without escaping")
	}
}

func TestHTMXRequestReturnsWorkspaceFragment(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	handler := auth.AuthMiddleware(strings.Repeat("s", 32), true)(http.HandlerFunc(New(s).Index))
	req := httptest.NewRequest(http.MethodGet, "/?view=audit", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("X-Ternal-User", "operator@example.com")
	req.Header.Set("X-Ternal-Groups", "ternal-admins")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	body := res.Body.String()
	if res.Code != http.StatusOK || !strings.HasPrefix(body, `<section id="workspace"`) {
		t.Fatalf("expected workspace fragment, status=%d body=%s", res.Code, body)
	}
	if !strings.Contains(body, `id="workspace-navigation"`) || !strings.Contains(body, `hx-swap-oob="outerHTML"`) || !strings.Contains(body, `aria-current="page"`) {
		t.Fatal("HTMX fragment did not include the current navigation OOB update")
	}
	if strings.Contains(body, "<!doctype html>") || strings.Contains(body, `<script src=`) {
		t.Fatal("HTMX fragment unexpectedly contains the document shell")
	}
}

func TestPortalRejectsAdminViewsForRegularUsers(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	handler := auth.AuthMiddleware(strings.Repeat("s", 32), true)(http.HandlerFunc(New(s).Index))
	for _, view := range []string{"policies", "audit"} {
		req := httptest.NewRequest(http.MethodGet, "/?view="+view, nil)
		req.Header.Set("X-Ternal-User", "user@example.com")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusForbidden {
			t.Errorf("view %s status = %d, want %d", view, res.Code, http.StatusForbidden)
		}
	}
}

func TestElyAndBrandAssets(t *testing.T) {
	handler := Assets()
	for _, name := range []string{"app.css", "htmx.min.js", "theme.js", "ely-controls.css", "ely-patterns.css", "ely-controls.js", "ely-patterns.js"} {
		want, err := ui.Files.ReadFile("assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/assets/"+name, nil))
		if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), want) || res.Header().Get("ETag") == "" {
			t.Errorf("Ely asset %s not served intact: status=%d", name, res.Code)
		}
	}
	for _, asset := range []string{"ternal-logo-640.png", "ternal-icon-256.png"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/"+asset, nil))
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" || !bytes.HasPrefix(response.Body.Bytes(), []byte("\x89PNG\r\n\x1a\n")) {
			t.Errorf("brand asset %s not served as PNG", asset)
		}
	}
}

func TestAllViewsAndLandingUseEly(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	handler := auth.AuthMiddleware(strings.Repeat("s", 32), true, "ternal-admins")(http.HandlerFunc(New(s).Index))
	for _, admin := range []bool{false, true} {
		for _, view := range []string{"hosts", "keys", "policies", "access", "audit"} {
			if !admin && (view == "policies" || view == "audit") {
				continue
			}
			for _, fragment := range []bool{false, true} {
				req := httptest.NewRequest(http.MethodGet, "/?view="+view, nil)
				req.Header.Set("X-Ternal-User", "user@example.com")
				if admin {
					req.Header.Set("X-Ternal-Groups", "ternal-admins")
				}
				if fragment {
					req.Header.Set("HX-Request", "true")
				}
				res := httptest.NewRecorder()
				handler.ServeHTTP(res, req)
				body := res.Body.String()
				if res.Code != http.StatusOK || !strings.Contains(body, `class="ui-table"`) || !strings.Contains(body, `data-slot="button"`) {
					t.Fatalf("view=%s admin=%v fragment=%v: %d %s", view, admin, fragment, res.Code, body)
				}
				if !admin && (strings.Contains(body, `hx-get="/?view=policies"`) || strings.Contains(body, `hx-get="/?view=audit"`)) {
					t.Fatal("admin navigation exposed")
				}
				if fragment == strings.Contains(body, "<!doctype html>") {
					t.Fatal("incorrect document/fragment response")
				}
			}
		}
	}
	res := httptest.NewRecorder()
	New(s).Index(res, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `href="/auth/login"`) || !strings.Contains(res.Body.String(), `data-slot="button"`) {
		t.Fatalf("landing: %d %s", res.Code, res.Body.String())
	}
}
