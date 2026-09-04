package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"

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
		Name:       `<script>alert("x")</script>`,
		EndpointID: strings.Repeat("a", 64),
		SSHUser:    "ops",
		SSHPort:    22,
	}); err != nil {
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
		`hx-get="/?view=policies"`,
		`hx-target="#workspace"`,
		`id="workspace" tabindex="-1"`,
		`aria-current="page"`,
		`aria-label="Registered SSH hosts"`,
		`name="htmx-config" content="{&#34;noSwap&#34;:[204,304,&#34;4xx&#34;,&#34;5xx&#34;]}"`,
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

func TestPinnedAssetsAndHTMXV4Attributes(t *testing.T) {
	javascript, err := assets.ReadFile("static/htmx.min.js")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(javascript)
	if got, want := hex.EncodeToString(sum[:]), "e484d9171a9db30a39c8f16e3d709d4137f3211c659f8e6125816635033d593f"; got != want {
		t.Fatalf("htmx asset digest = %s, want %s", got, want)
	}
	css, err := assets.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(css, []byte(".bg-ink-900")) {
		t.Fatal("Tailwind did not scan gomponents classes")
	}

	var rendered bytes.Buffer
	if err := h.Div(g.Attr("hx-confirm:inherited", "Continue?")).Render(&rendered); err != nil {
		t.Fatal(err)
	}
	if rendered.String() != `<div hx-confirm:inherited="Continue?"></div>` {
		t.Fatalf("unexpected htmx v4 attribute output: %s", rendered.String())
	}
}
