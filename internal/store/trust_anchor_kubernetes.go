package store

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type kubernetesTrustAnchor struct {
	name, namespace, baseURL, tokenPath string
	client                              *http.Client
}

var kubernetesAnchorBackend = func(name, namespace string) trustAnchorBackend {
	return newKubernetesTrustAnchor(name, namespace)
}

type configMapWire struct {
	APIVersion string                     `json:"apiVersion,omitempty"`
	Kind       string                     `json:"kind,omitempty"`
	Metadata   map[string]json.RawMessage `json:"metadata"`
	Data       map[string]string          `json:"data"`
}

func (w *configMapWire) metadataString(key string) string {
	var value string
	_ = json.Unmarshal(w.Metadata[key], &value)
	return value
}

func (w *configMapWire) setMetadataString(key, value string) {
	if w.Metadata == nil {
		w.Metadata = make(map[string]json.RawMessage)
	}
	w.Metadata[key], _ = json.Marshal(value)
}

func newKubernetesTrustAnchor(name, namespace string) *kubernetesTrustAnchor {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS")
	if port == "" {
		port = os.Getenv("KUBERNETES_SERVICE_PORT")
	}
	if host == "" {
		host = "kubernetes.default.svc"
	}
	if port == "" {
		port = "443"
	}
	caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	pem, _ := os.ReadFile(caPath)
	roots := x509.NewCertPool()
	if len(pem) > 0 {
		roots.AppendCertsFromPEM(pem)
	}
	return &kubernetesTrustAnchor{name: name, namespace: namespace, baseURL: "https://" + host + ":" + port, tokenPath: "/var/run/secrets/kubernetes.io/serviceaccount/token", client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}}
}

func (k *kubernetesTrustAnchor) path() string {
	return k.baseURL + "/api/v1/namespaces/" + url.PathEscape(k.namespace) + "/configmaps/" + url.PathEscape(k.name)
}
func (k *kubernetesTrustAnchor) request(ctx context.Context, method string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, k.path(), body)
	if err != nil {
		return nil, err
	}
	token, err := os.ReadFile(k.tokenPath)
	if err != nil {
		return nil, fmt.Errorf("read service-account token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return k.client.Do(req)
}
func (k *kubernetesTrustAnchor) Get(ctx context.Context) (trustAnchorRecord, string, error) {
	wire, rv, err := k.getWire(ctx)
	if err != nil {
		return trustAnchorRecord{}, "", err
	}
	record, err := parseAnchorData(wire.Data)
	return record, rv, err
}

func (k *kubernetesTrustAnchor) getWire(ctx context.Context) (configMapWire, string, error) {
	response, err := k.request(ctx, http.MethodGet, nil)
	if err != nil {
		return configMapWire{}, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return configMapWire{}, "", fmt.Errorf("kubernetes status %d", response.StatusCode)
	}
	var wire configMapWire
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&wire); err != nil {
		return configMapWire{}, "", err
	}
	rv := wire.metadataString("resourceVersion")
	if rv == "" || wire.metadataString("name") != k.name || wire.metadataString("namespace") != k.namespace {
		return configMapWire{}, "", fmt.Errorf("invalid trust-anchor metadata")
	}
	return wire, rv, nil
}
func (k *kubernetesTrustAnchor) CAS(ctx context.Context, rv string, next trustAnchorRecord) (string, error) {
	if rv == "" {
		return "", fmt.Errorf("missing resource version")
	}
	// Preserve admission-controlled metadata.  A GET before PUT is still a
	// resourceVersion CAS: any intervening change makes the PUT conflict.
	wire, actualRV, err := k.getWire(ctx)
	if err != nil {
		return "", err
	}
	if actualRV != rv {
		return "", fmt.Errorf("trust anchor resource version conflict")
	}
	wire.Data = anchorData(next)
	wire.setMetadataString("resourceVersion", rv)
	wire.setMetadataString("name", k.name)
	wire.setMetadataString("namespace", k.namespace)
	payload, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	response, err := k.request(ctx, http.MethodPut, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("kubernetes status %d", response.StatusCode)
	}
	var result configMapWire
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return "", err
	}
	updated := result.metadataString("resourceVersion")
	if updated == "" {
		return "", fmt.Errorf("missing updated resource version")
	}
	return updated, nil
}
