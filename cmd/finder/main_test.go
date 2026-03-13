package main

import (
	"crypto/rsa"
	"testing"

	common "github.com/oracle/oci-go-sdk/v65/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type mockProvider struct {
	mock.Mock
}

func (m *mockProvider) TenancyOCID() (string, error)    { return "", nil }
func (m *mockProvider) UserOCID() (string, error)       { return "", nil }
func (m *mockProvider) KeyFingerprint() (string, error) { return "", nil }
func (m *mockProvider) Region() (string, error)         { return "", nil }
func (m *mockProvider) PrivateRSAKey() (*rsa.PrivateKey, error) {
	return nil, nil
}
func (m *mockProvider) KeyPassphrase() (*string, error)      { return nil, nil }
func (m *mockProvider) AuthType() (common.AuthConfig, error) { return common.AuthConfig{}, nil }
func (m *mockProvider) KeyID() (string, error)               { return "", nil }

func TestRegionFromOCID(t *testing.T) {
	provider := &mockProvider{}

	testCases := []struct {
		name   string
		ocid   string
		want   string
		wantOK bool
	}{
		{"standardRegion", "ocid1.instance.oc1.us-phoenix-1.example", "us-phoenix-1", true},
		{"shortAliasPhx", "ocid1.instance.oc1.phx.example", "us-phoenix-1", true},
		{"caMontrealRegion", "ocid1.vcn.oc1.ca-montreal-1.ama", "ca-montreal-1", true},
		{"shortAliasIad", "ocid1.instance.oc1.iad.example", "us-ashburn-1", true},
		{"usesKnownGov", "ocid1.bucket.oc1.us-gov-phoenix-1.someid", "us-gov-phoenix-1", true},
		{"invalidNoRegion", "ocid1.compartment.oc1..aaaa", "", false},
		{"invalidGarbageToken", "ocid1.instance.oc1.x-y-z.example", "", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := regionFromOCID(tc.ocid, provider)
			require.Equal(t, tc.wantOK, ok, "unexpected match result for %s", tc.ocid)
			require.Equal(t, tc.want, got, "unexpected region for %s", tc.ocid)
		})
	}
}
