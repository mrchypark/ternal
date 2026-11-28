package web

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mrchypark/ternal/internal/auth"
	"github.com/mrchypark/ternal/internal/store"
)

const behaviorSessionKey = "behavior-test-session-key-123456"

func TestPortalRegularUserDataIsolation(t *testing.T) {
	s := newBehaviorStore(t)
	ctx := context.Background()

	if _, err := s.CreateHost(ctx, store.NewHost{Name: "visible-to-operators", EndpointID: strings.Repeat("a", 64), SSHUser: "ops", Status: "online"}, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateHost(ctx, store.NewHost{Name: "hidden-from-operators", EndpointID: strings.Repeat("b", 64), SSHUser: "ops", Status: "online"}, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePolicy(ctx, store.NewPolicy{Name: "operator-hosts", Principal: "operators", HostSelector: "visible-*", SSHUsers: []string{"ops"}}, "system"); err != nil {
		t.Fatal(err)
	}

	activeHostID := newBehaviorActiveHost(t, s)
	if _, err := s.CreateSSHKey(ctx, "alice@example.com", "alice-own-key-marker", "alice-own-fingerprint"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSSHKey(ctx, "bob@example.com", "bob-private-key-marker", "bob-private-fingerprint"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueSSHAccess(ctx, "alice@example.com", activeHostID, "alice-own-grant-marker", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccessRequest(ctx, "alice@example.com", activeHostID, "alice-own-request-marker"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueSSHAccess(ctx, "bob@example.com", activeHostID, "bob-private-grant-marker", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccessRequest(ctx, "bob@example.com", activeHostID, "bob-private-request-marker"); err != nil {
		t.Fatal(err)
	}

	for _, check := range []struct {
		view     string
		contains []string
		omits    []string
	}{
		{"hosts", []string{"visible-to-operators"}, []string{"hidden-from-operators"}},
		{"keys", []string{"alice-own-key-marker", "alice-own-fingerprint"}, []string{"bob-private-key-marker", "bob-private-fingerprint"}},
		{"access", []string{"alice-own-grant-marker", "alice-own-request-marker"}, []string{"bob-private-grant-marker", "bob-private-request-marker"}},
	} {
		body := behaviorPage(t, s, check.view, "alice@example.com", "operators")
		for _, value := range check.contains {
			if !strings.Contains(body, value) {
				t.Errorf("view %q omitted %q", check.view, value)
			}
		}
		for _, value := range check.omits {
			if strings.Contains(body, value) {
				t.Errorf("view %q exposed %q", check.view, value)
			}
		}
	}
}

func TestPortalAdministratorSeesPopulatedTables(t *testing.T) {
	s := newBehaviorStore(t)
	ctx := context.Background()

	if _, err := s.CreateHost(ctx, store.NewHost{Name: "admin-visible-host", EndpointID: strings.Repeat("c", 64), SSHUser: "ops"}, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePolicy(ctx, store.NewPolicy{Name: "admin-visible-policy", Principal: "operators", HostSelector: "*", SSHUsers: []string{"ops"}}, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSSHKey(ctx, "admin@example.com", "admin-visible-key", "admin-visible-fingerprint"); err != nil {
		t.Fatal(err)
	}
	activeHostID := newBehaviorActiveHost(t, s)
	if _, err := s.IssueSSHAccess(ctx, "other-user", activeHostID, "admin-visible-grant", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccessRequest(ctx, "other-user", activeHostID, "admin-visible-request"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAuditEvent(ctx, "admin-visible-audit", "host", activeHostID, "other-user", nil, nil); err != nil {
		t.Fatal(err)
	}

	for _, check := range []struct{ view, marker string }{
		{"hosts", "admin-visible-host"},
		{"keys", "admin-visible-key"},
		{"policies", "admin-visible-policy"},
		{"access", "admin-visible-grant"},
		{"audit", "admin-visible-audit"},
	} {
		if body := behaviorPage(t, s, check.view, "admin@example.com", "ternal-admins"); !strings.Contains(body, check.marker) {
			t.Errorf("administrator %q view omitted %q", check.view, check.marker)
		}
	}
}

func TestPortalEscapesStoredTableValues(t *testing.T) {
	s := newBehaviorStore(t)
	ctx := context.Background()

	if _, err := s.CreateHost(ctx, store.NewHost{Name: "escape-host", EndpointID: strings.Repeat("d", 64), SSHUser: "ops", Status: "<host-status>"}, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSSHKey(ctx, "admin@example.com", "<key-public>", "<key-fingerprint>"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePolicy(ctx, store.NewPolicy{Name: "<policy-name>", Principal: "<policy-principal>", HostSelector: "<policy-host-selector>", SSHUsers: []string{"<policy-ssh-user>"}}, "system"); err != nil {
		t.Fatal(err)
	}
	activeHostID := newBehaviorActiveHost(t, s)
	if _, err := s.IssueSSHAccess(ctx, "<grant-user>", activeHostID, "<grant-ssh-user>", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccessRequest(ctx, "<request-user>", activeHostID, "<request-ssh-user>"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAuditEvent(ctx, "<audit-action>", "<audit-resource>", "<audit-id>", "<audit-user>", nil, nil); err != nil {
		t.Fatal(err)
	}

	for _, check := range []struct {
		view   string
		values []string
	}{
		{"hosts", []string{"<host-status>"}},
		{"keys", []string{"<key-public>", "<key-fingerprint>"}},
		{"policies", []string{"<policy-name>", "<policy-principal>", "<policy-host-selector>", "<policy-ssh-user>"}},
		{"access", []string{"<grant-user>", "<grant-ssh-user>", "<request-user>", "<request-ssh-user>"}},
		{"audit", []string{"<audit-action>", "<audit-resource>", "<audit-id>", "<audit-user>"}},
	} {
		body := behaviorPage(t, s, check.view, "admin@example.com", "ternal-admins")
		for _, value := range check.values {
			escaped := strings.ReplaceAll(strings.ReplaceAll(value, "<", "&lt;"), ">", "&gt;")
			if !strings.Contains(body, escaped) {
				t.Errorf("view %q did not escape %q", check.view, value)
			}
			if strings.Contains(body, value) {
				t.Errorf("view %q rendered %q without escaping", check.view, value)
			}
		}
	}
}

func newBehaviorStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newBehaviorActiveHost(t *testing.T, s *store.Store) string {
	t.Helper()
	expires := time.Now().Add(time.Hour).Unix()
	token, err := s.CreateManufacturingToken(t.Context(), "", &expires, "system")
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	device, err := s.EnrollDevice(t.Context(), token.Token, strings.Repeat("9", 64), "BEHAVIOR-000001", "test", "SHA256:"+strings.Repeat("A", 43), base64.StdEncoding.EncodeToString(public), "ops", 22, nil)
	if err != nil {
		t.Fatal(err)
	}
	return device.HostID
}

func behaviorPage(t *testing.T, s *store.Store, view, user, groups string) string {
	t.Helper()
	handler := auth.AuthMiddleware(behaviorSessionKey, true, "ternal-admins")(http.HandlerFunc(New(s).Index))
	req := httptest.NewRequest(http.MethodGet, "/?view="+view, nil)
	req.Header.Set("X-Ternal-User", user)
	req.Header.Set("X-Ternal-Groups", groups)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("view %q status = %d, body = %s", view, res.Code, res.Body.String())
	}
	return res.Body.String()
}
