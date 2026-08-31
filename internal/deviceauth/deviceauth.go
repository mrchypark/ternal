package deviceauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const MaxClockSkew = 5 * time.Minute

type Discovery struct {
	DirectAddresses []string `json:"direct_addresses"`
	RelayURLs       []string `json:"relay_urls"`
}

type Identity struct {
	Serial             string `json:"serial"`
	HostKeyFingerprint string `json:"ssh_host_key_fingerprint"`
}

func GenerateKey(path string) (string, error) {
	if path == "" {
		return "", errors.New("device key path is required")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	if err := writePrivate(path, private); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(public), nil
}

func LoadPrivate(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid device private key")
	}
	return ed25519.PrivateKey(decoded), nil
}

func PublicKey(private ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(private.Public().(ed25519.PublicKey))
}

func Sign(private ed25519.PrivateKey, payload string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(private, []byte(payload)))
}

func Verify(publicKey, payload, signature string) error {
	public, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return errors.New("invalid device public key")
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(public), []byte(payload), sig) {
		return errors.New("invalid device signature")
	}
	return nil
}

func Fresh(timestamp int64, now time.Time) bool {
	delta := now.Unix() - timestamp
	if delta < 0 {
		delta = -delta
	}
	return delta <= int64(MaxClockSkew/time.Second)
}

func HeartbeatPayload(serial, endpointID, fingerprint string, timestamp int64, status string, discovery *Discovery) string {
	base := fmt.Sprintf("heartbeat\n%s\n%s\n%s\n%d\n%s", serial, endpointID, fingerprint, timestamp, status)
	if discovery == nil {
		return base
	}
	encoded, _ := json.Marshal(discovery)
	return base + "\n" + string(encoded)
}

func AuthorizedKeysPayload(serial, endpointID, fingerprint string, timestamp int64, sshUser string) string {
	return fmt.Sprintf("authorized-keys\n%s\n%s\n%s\n%d\n%s", serial, endpointID, fingerprint, timestamp, sshUser)
}

func AuthorizedKeysAckPayload(serial, endpointID, fingerprint string, timestamp int64, sshUser string, generation int64, digest string) string {
	return fmt.Sprintf("authorized-keys-ack\n%s\n%s\n%s\n%d\n%s\n%d\n%s", serial, endpointID, fingerprint, timestamp, sshUser, generation, digest)
}

func IdentityPath(keyPath string) string { return keyPath + ".identity.json" }

func WriteIdentity(path string, identity Identity) error {
	if identity.Serial == "" || identity.HostKeyFingerprint == "" {
		return errors.New("invalid device identity")
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'), 0600)
}

func ReadIdentity(path string) (Identity, error) {
	var identity Identity
	data, err := os.ReadFile(path)
	if err != nil {
		return identity, err
	}
	if err := json.Unmarshal(data, &identity); err != nil || identity.Serial == "" || identity.HostKeyFingerprint == "" {
		return Identity{}, errors.New("invalid device identity")
	}
	return identity, nil
}

func writePrivate(path string, private ed25519.PrivateKey) error {
	return atomicWrite(path, []byte(base64.StdEncoding.EncodeToString(private)), 0600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".ternal-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
