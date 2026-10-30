package store

import (
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/mrchypark/rhiza"
	"github.com/thanos-io/objstore/providers/azure"
)

var azureAccountName = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
var azureDNSName = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*$`)

type azureStorageConfig struct {
	Account, BlobEndpoint, AccountKey, ConnectionString string
	TenantID, ClientID, ClientSecret, UserAssignedID    string
	Emulator                                            bool
}

func azureStorageFromEnv() (azureStorageConfig, error) {
	a := azureStorageConfig{
		Account:          os.Getenv("TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT"),
		BlobEndpoint:     os.Getenv("TERNAL_OBJECT_STORE_AZURE_BLOB_ENDPOINT"),
		AccountKey:       os.Getenv("TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT_KEY"),
		ConnectionString: os.Getenv("TERNAL_OBJECT_STORE_AZURE_CONNECTION_STRING"),
		TenantID:         os.Getenv("TERNAL_OBJECT_STORE_AZURE_TENANT_ID"),
		ClientID:         os.Getenv("TERNAL_OBJECT_STORE_AZURE_CLIENT_ID"),
		ClientSecret:     os.Getenv("TERNAL_OBJECT_STORE_AZURE_CLIENT_SECRET"),
		UserAssignedID:   os.Getenv("TERNAL_OBJECT_STORE_AZURE_USER_ASSIGNED_ID"),
		Emulator:         os.Getenv("TERNAL_OBJECT_STORE_AZURE_EMULATOR") == "1",
	}
	if value := os.Getenv("TERNAL_OBJECT_STORE_AZURE_EMULATOR"); value != "" && value != "0" && value != "1" {
		return a, errors.New("TERNAL_OBJECT_STORE_AZURE_EMULATOR must be 0 or 1")
	}
	if os.Getenv("TERNAL_OBJECT_STORE_ENDPOINT") != "" || (os.Getenv("TERNAL_OBJECT_STORE_INSECURE") != "" && os.Getenv("TERNAL_OBJECT_STORE_INSECURE") != "false") {
		return a, errors.New("Azure rejects generic endpoint and insecure overrides")
	}
	return a.normalized()
}

func (a azureStorageConfig) normalized() (azureStorageConfig, error) {
	if !azureAccountName.MatchString(a.Account) {
		return a, errors.New("Azure requires a canonical TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT")
	}
	if a.BlobEndpoint == "" {
		if a.Emulator {
			return a, errors.New("Azure emulator requires an explicit loopback Blob endpoint")
		}
		a.BlobEndpoint = "https://" + a.Account + ".blob.core.windows.net"
	}
	u, err := url.Parse(a.BlobEndpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.Host == "" || u.RawPath != "" || u.Host != strings.ToLower(u.Host) || u.String() != a.BlobEndpoint || strings.ContainsAny(a.BlobEndpoint, "\\#\x00\r\n\t ") {
		return a, errors.New("Azure Blob endpoint must be a canonical credential-free service URL")
	}
	if strings.HasSuffix(u.Host, ":") {
		return a, errors.New("Azure Blob endpoint must not contain an empty port")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return a, errors.New("Azure Blob endpoint port must be between 1 and 65535")
		}
	}
	if a.Emulator {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || u.Port() == "" || u.Path != "/"+a.Account || a.ConnectionString == "" {
			return a, errors.New("Azure emulator requires a connection string and explicit http://loopback:PORT/account endpoint")
		}
	} else if u.Scheme != "https" || u.Path != "" || !azureDNSName.MatchString(u.Hostname()) {
		return a, errors.New("Azure requires a canonical HTTPS Blob service endpoint")
	}
	forms := 0
	for _, present := range []bool{a.AccountKey != "", a.ConnectionString != "", a.TenantID != "" || a.ClientID != "" || a.ClientSecret != "", a.UserAssignedID != ""} {
		if present {
			forms++
		}
	}
	if forms > 1 {
		return a, errors.New("Azure authentication forms must not be mixed")
	}
	if (a.TenantID != "" || a.ClientID != "" || a.ClientSecret != "") && (a.TenantID == "" || a.ClientID == "" || a.ClientSecret == "") {
		return a, errors.New("Azure tenant, client and client secret must be set together")
	}
	if a.AccountKey != "" {
		if !validAzureAccountKey(a.AccountKey) {
			return a, errors.New("invalid Azure storage account key encoding")
		}
	}
	if a.ConnectionString != "" {
		if err := a.validateConnectionString(); err != nil {
			return a, err
		}
	} else if u.Port() != "" || !strings.HasPrefix(u.Hostname(), a.Account+".") || !azureDNSName.MatchString(strings.TrimPrefix(u.Hostname(), a.Account+".")) {
		return a, errors.New("Azure endpoint is not representable by the SDK account/suffix configuration")
	}
	return a, nil
}

// Only account-key connection strings are accepted; SAS and implicit development
// shortcuts cannot supply the credential-free account/endpoint trust binding.
func (a azureStorageConfig) validateConnectionString() error {
	invalid := errors.New("Azure connection string is invalid or does not match the declared account and Blob endpoint")
	fields := map[string]string{}
	for _, item := range strings.Split(strings.TrimSuffix(a.ConnectionString, ";"), ";") {
		key, value, ok := strings.Cut(item, "=")
		if !ok || value == "" || fields[key] != "" {
			return invalid
		}
		switch key {
		case "AccountName", "AccountKey", "BlobEndpoint", "DefaultEndpointsProtocol", "EndpointSuffix":
			fields[key] = value
		default:
			return invalid
		}
	}
	if fields["AccountName"] != a.Account || fields["AccountKey"] == "" {
		return invalid
	}
	if !validAzureAccountKey(fields["AccountKey"]) {
		return invalid
	}
	protocol := fields["DefaultEndpointsProtocol"]
	if protocol == "" {
		protocol = "https"
	}
	if protocol != "https" && protocol != "http" {
		return invalid
	}
	endpoint := fields["BlobEndpoint"]
	if endpoint == "" {
		suffix := fields["EndpointSuffix"]
		if suffix == "" {
			suffix = "core.windows.net"
		}
		if !azureDNSName.MatchString(suffix) {
			return invalid
		}
		endpoint = protocol + "://" + a.Account + ".blob." + suffix
	} else if fields["EndpointSuffix"] != "" || (fields["DefaultEndpointsProtocol"] != "" && !strings.HasPrefix(endpoint, protocol+"://")) {
		return invalid
	}
	if endpoint != a.BlobEndpoint {
		return invalid
	}
	return nil
}

func validAzureAccountKey(key string) bool {
	decoded, err := base64.StdEncoding.DecodeString(key)
	return err == nil && len(decoded) > 0 && !strings.ContainsAny(key, "\r\n")
}

func (a azureStorageConfig) sdkEndpoint() string {
	u, _ := url.Parse(a.BlobEndpoint) // normalized before SDK projection
	if u.Scheme == "https" && u.Path == "" && u.Port() == "" && strings.HasPrefix(u.Hostname(), a.Account+".") {
		return strings.TrimPrefix(u.Hostname(), a.Account+".")
	}
	// Only connection strings can reach this case; the SDK ignores Endpoint
	// then, while Rhiza's local identity still needs to bind the actual target.
	return a.BlobEndpoint
}

func (a azureStorageConfig) apply(c *rhiza.Config) {
	c.ObjStoreAzureStorageAccount, c.ObjStoreAzureStorageAccountKey = a.Account, a.AccountKey
	c.ObjStoreAzureConnectionString = a.ConnectionString
	c.ObjStoreAzureTenantID, c.ObjStoreAzureClientID, c.ObjStoreAzureClientSecret = a.TenantID, a.ClientID, a.ClientSecret
	c.ObjStoreAzureUserAssignedID = a.UserAssignedID
	c.ObjStoreEndpoint = a.sdkEndpoint()
}

func (a azureStorageConfig) bucketConfig(container string) azure.Config {
	c := azure.DefaultConfig
	c.StorageCreateContainer = false
	c.StorageAccountName, c.StorageAccountKey, c.StorageConnectionString = a.Account, a.AccountKey, a.ConnectionString
	c.AzTenantID, c.ClientID, c.ClientSecret, c.UserAssignedID = a.TenantID, a.ClientID, a.ClientSecret, a.UserAssignedID
	c.ContainerName = container
	c.Endpoint = a.sdkEndpoint()
	return c
}

func applyAzureStorageFromEnv(c *rhiza.Config) error {
	if c.ObjStoreProvider != "azure" {
		return nil
	}
	a, err := azureStorageFromEnv()
	if err != nil {
		return err
	}
	if c.ObjStoreBucket == "" {
		return errors.New("Azure requires TERNAL_OBJECT_STORE_BUCKET as its container")
	}
	a.apply(c)
	return nil
}

func azureEndpointFromRhiza(c rhiza.Config) string {
	if c.ObjStoreAzureConnectionString != "" {
		for _, item := range strings.Split(c.ObjStoreAzureConnectionString, ";") {
			if key, value, ok := strings.Cut(item, "="); ok && key == "BlobEndpoint" {
				return value
			}
		}
		protocol, suffix := "https", "core.windows.net"
		for _, item := range strings.Split(c.ObjStoreAzureConnectionString, ";") {
			key, value, _ := strings.Cut(item, "=")
			if key == "DefaultEndpointsProtocol" {
				protocol = value
			}
			if key == "EndpointSuffix" {
				suffix = value
			}
		}
		return protocol + "://" + c.ObjStoreAzureStorageAccount + ".blob." + suffix
	}
	suffix := c.ObjStoreEndpoint
	if suffix == "" {
		suffix = "blob.core.windows.net"
	}
	return "https://" + c.ObjStoreAzureStorageAccount + "." + suffix
}
