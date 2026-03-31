package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"errors"
	"log/slog"
	"testing"

	common "github.com/oracle/oci-go-sdk/v65/common"
	search "github.com/oracle/oci-go-sdk/v65/resourcesearch"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type mockProvider struct {
	mock.Mock
	region    string
	regionErr error
}

func (m *mockProvider) TenancyOCID() (string, error)    { return "", nil }
func (m *mockProvider) UserOCID() (string, error)       { return "", nil }
func (m *mockProvider) KeyFingerprint() (string, error) { return "", nil }
func (m *mockProvider) Region() (string, error) {
	if m.regionErr != nil {
		return "", m.regionErr
	}
	return m.region, nil
}
func (m *mockProvider) PrivateRSAKey() (*rsa.PrivateKey, error) {
	return nil, nil
}
func (m *mockProvider) KeyPassphrase() (*string, error)      { return nil, nil }
func (m *mockProvider) AuthType() (common.AuthConfig, error) { return common.AuthConfig{}, nil }
func (m *mockProvider) KeyID() (string, error)               { return "", nil }

type stubSearchClient struct {
	region     string
	items      []search.ResourceSummary
	respErr    error
	lastQuery  string
	callCount  int
	limitValue *int
}

func (s *stubSearchClient) SetRegion(region string) {
	s.region = region
}

func (s *stubSearchClient) SearchResources(_ context.Context, req search.SearchResourcesRequest) (search.SearchResourcesResponse, error) {
	s.callCount++
	if details, ok := req.SearchDetails.(search.StructuredSearchDetails); ok {
		if details.Query != nil {
			s.lastQuery = *details.Query
		}
	}
	s.limitValue = req.Limit
	resp := search.SearchResourcesResponse{}
	resp.RawResponse = nil
	resp.ResourceSummaryCollection = search.ResourceSummaryCollection{
		Items: s.items,
	}
	return resp, s.respErr
}

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

func TestProcessResourceID(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		client := &stubSearchClient{
			items: []search.ResourceSummary{{Identifier: common.String("example")}},
		}
		provider := &mockProvider{}
		result := processResourceID(context.Background(), client, provider, "us-phoenix-1", "instance", "ocid1.example", nil)

		require.Equal(t, "us-phoenix-1", result.Region)
		require.Equal(t, 1, len(result.Resources))
		require.Equal(t, "", result.Message)
		require.Equal(t, "query instance resources where identifier = 'ocid1.example'", client.lastQuery)
	})

	t.Run("search error added to message", func(t *testing.T) {
		client := &stubSearchClient{
			respErr: errors.New("boom"),
		}
		provider := &mockProvider{}
		result := processResourceID(context.Background(), client, provider, "us-phoenix-1", "instance", "ocid1.err", nil)

		require.Contains(t, result.Message, "search resources: boom")
		require.Nil(t, result.Resources)
	})

	t.Run("no results still recorded", func(t *testing.T) {
		client := &stubSearchClient{}
		provider := &mockProvider{}
		result := processResourceID(context.Background(), client, provider, "us-phoenix-1", "instance", "ocid1.none", nil)

		require.Equal(t, "No resources found matching the criteria.", result.Message)
		require.Nil(t, result.Resources)
	})

	t.Run("region resolution error stored", func(t *testing.T) {
		provider := &mockProvider{regionErr: errors.New("bad region")}
		client := &stubSearchClient{}
		result := processResourceID(context.Background(), client, provider, "", "instance", "ocid1.none", nil)

		require.Contains(t, result.Message, "resolve region: determine region from profile: bad region")
	})
}

type fakeTokenPool struct {
	tokens chan struct{}
}

func newFakeTokenPool(size int) *fakeTokenPool {
	return &fakeTokenPool{tokens: make(chan struct{}, size)}
}

func (f *fakeTokenPool) Acquire(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case f.tokens <- struct{}{}:
		return true
	}
}

func (f *fakeTokenPool) Token() bool {
	select {
	case <-f.tokens:
		return true
	default:
		return false
	}
}

func TestProcessResourceIDRateLimit(t *testing.T) {
	buf, restore := withTestLogger()
	defer restore()
	acquire := func(context.Context) bool { return false }
	client := &stubSearchClient{}
	provider := &mockProvider{}
	result := processResourceID(context.Background(), client, provider, "us-phoenix-1", "instance", "ocid1.rate", acquire)
	require.Equal(t, "rate limit not available", result.Message)
	require.Contains(t, buf.String(), "rate limit not available")
}

func TestNormalizeResourceIDs(t *testing.T) {
	testCases := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "already clean",
			input: []string{"ocid1", "ocid2"},
			want:  []string{"ocid1", "ocid2"},
		},
		{
			name:  "quoted with newlines",
			input: []string{"\"ocid1\"\n\"ocid2\"\n\"ocid3\""},
			want:  []string{"ocid1", "ocid2", "ocid3"},
		},
		{
			name:  "single quotes and spaces",
			input: []string{"'ocid1' 'ocid2'"},
			want:  []string{"ocid1", "ocid2"},
		},
		{
			name:  "mixed clean and dirty",
			input: []string{"ocid1", "\"ocid2\""},
			want:  []string{"ocid1", "ocid2"},
		},
		{
			name:  "empty after cleaning",
			input: []string{"\"\""},
			want:  nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeResourceIDs(tc.input)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEffectiveWorkerCount(t *testing.T) {
	testCases := []struct {
		name      string
		requested int
		total     int
		want      int
	}{
		{"requestedZero", 0, 5, 1},
		{"requestedNegative", -3, 4, 1},
		{"requestedWithinTotal", 3, 5, 3},
		{"requestedExceedsTotal", 10, 2, 2},
		{"noResources", 5, 0, 5},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveWorkerCount(tc.requested, tc.total)
			require.Equal(t, tc.want, got)
		})
	}
}

func withTestLogger() (*bytes.Buffer, func()) {
	buf := &bytes.Buffer{}
	prev := logger
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger = slog.New(handler)
	return buf, func() {
		logger = prev
	}
}
