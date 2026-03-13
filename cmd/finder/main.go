package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	common "github.com/oracle/oci-go-sdk/v65/common"
	identity "github.com/oracle/oci-go-sdk/v65/identity"
	search "github.com/oracle/oci-go-sdk/v65/resourcesearch"
)

var debugEnabled bool

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "OCI Structured Search CLI\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [--resource-type <type>] [--region <region>] [--profile <profile>] <resource-id>\n\n", os.Args[0])
		flag.PrintDefaults()
	}

	region := flag.String("region", "", "OCI region to target (defaults to profile region)")
	profile := flag.String("profile", "DEFAULT", "OCI CLI profile name from ~/.oci/config")
	resourceType := flag.String("resource-type", "all", "OCI resource type to search (defaults to all)")
	flag.BoolVar(&debugEnabled, "debug", false, "Enable verbose debug logging")

	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		flag.Usage()
		fmt.Fprintln(os.Stderr, "error: resource-id positional argument is required")
		os.Exit(1)
	}
	resourceID := args[0]

	if err := run(*region, *profile, *resourceType, resourceID); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(region, profile, resourceType, resourceID string) error {
	configProvider, err := configurationProvider(profile)
	if err != nil {
		return fmt.Errorf("load OCI config: %w", err)
	}

	regionToUse, err := resolveRegion(region, resourceID, configProvider)
	if err != nil {
		return err
	}
	debugf("Using region %s", regionToUse)

	client, err := search.NewResourceSearchClientWithConfigurationProvider(configProvider)
	if err != nil {
		return fmt.Errorf("create resource search client: %w", err)
	}
	client.SetRegion(regionToUse)

	resourceScope := resourceType
	if resourceScope == "" {
		resourceScope = "all"
	}
	query := fmt.Sprintf("query %s resources where identifier = '%s'", resourceScope, resourceID)
	request := search.SearchResourcesRequest{
		SearchDetails: search.StructuredSearchDetails{
			MatchingContextType: search.SearchDetailsMatchingContextTypeHighlights,
			Query:               common.String(query),
		},
		Limit: common.Int(25),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	response, err := client.SearchResources(ctx, request)
	if err != nil {
		return fmt.Errorf("search resources: %w", err)
	}

	if len(response.Items) == 0 {
		fmt.Println("No resources found matching the criteria.")
		return nil
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(response.Items)
}

func configurationProvider(profile string) (common.ConfigurationProvider, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("determine home directory: %w", err)
	}
	configPath := filepath.Join(home, ".oci", "config")
	return common.ConfigurationProviderFromFileWithProfile(configPath, profile, "")
}

func resolveRegion(flagRegion, resourceID string, provider common.ConfigurationProvider) (string, error) {
	if flagRegion != "" {
		debugf("Region provided via flag: %s", flagRegion)
		return flagRegion, nil
	}

	if ocidRegion, ok := regionFromOCID(resourceID, provider); ok {
		debugf("Region derived from OCID: %s", ocidRegion)
		return ocidRegion, nil
	}

	profileRegion, err := provider.Region()
	if err != nil {
		return "", fmt.Errorf("determine region from profile: %w", err)
	}
	debugf("Falling back to profile region: %s", profileRegion)
	return profileRegion, nil
}

var knownRegions = map[string]struct{}{
	"us-phoenix-1":         {},
	"us-ashburn-1":         {},
	"us-sanjose-1":         {},
	"us-chicago-1":         {},
	"us-austin-1":          {},
	"ca-toronto-1":         {},
	"ca-montreal-1":        {},
	"sa-saopaulo-1":        {},
	"sa-vinhedo-1":         {},
	"uk-london-1":          {},
	"uk-cardiff-1":         {},
	"eu-frankfurt-1":       {},
	"eu-zurich-1":          {},
	"eu-amsterdam-1":       {},
	"eu-milan-1":           {},
	"eu-marseille-1":       {},
	"eu-stockholm-1":       {},
	"me-jeddah-1":          {},
	"me-dubai-1":           {},
	"me-abudhabi-1":        {},
	"af-johannesburg-1":    {},
	"ap-mumbai-1":          {},
	"ap-hyderabad-1":       {},
	"ap-singapore-1":       {},
	"ap-kualalumpur-1":     {},
	"ap-tokyo-1":           {},
	"ap-osaka-1":           {},
	"ap-seoul-1":           {},
	"ap-chuncheon-1":       {},
	"ap-sydney-1":          {},
	"ap-melbourne-1":       {},
	"ap-auckland-1":        {},
	"ap-jakarta-1":         {},
	"mx-queretaro-1":       {},
	"ar-buenosaires-1":     {},
	"br-santiago-1":        {},
	"il-jerusalem-1":       {},
	"in-chennai-1":         {},
	"es-madrid-1":          {},
	"us-gov-phoenix-1":     {},
	"us-gov-ashburn-1":     {},
	"us-gov-chicago-1":     {},
	"us-gov-sanjose-1":     {},
	"uk-gov-london-1":      {},
	"uk-gov-cardiff-1":     {},
	"ap-osakainternal-1":   {},
	"us-langley-1":         {},
	"us-luke-1":            {},
	"us-dod-phoenix-1":     {},
	"us-dod-ashburn-1":     {},
	"us-dod-hawaii-1":      {},
	"us-dod-gov-ashburn-1": {},
}

var regionTokenRegexp = regexp.MustCompile(`^[a-z]+(?:-[a-z0-9]+)+-\d+$`)

func regionFromOCID(ocid string, provider common.ConfigurationProvider) (string, bool) {
	debugf("Attempting to derive region from OCID: %s", ocid)
	parts := strings.Split(ocid, ".")
	for _, part := range parts {
		candidate := strings.ToLower(part)
		if !regionTokenRegexp.MatchString(candidate) {
			debugf("Skipping token %s (not region-like)", candidate)
			continue
		}
		debugf("Evaluating candidate region token: %s", candidate)
		if _, ok := knownRegions[candidate]; ok {
			return candidate, true
		}
		debugf("Token %s not in known regions; checking Identity API", candidate)
		if regionFromAPI, ok := checkRegionsFromIdentity(provider, candidate); ok {
			return regionFromAPI, true
		}
	}
	return "", false
}

var (
	identityRegions     = map[string]struct{}{}
	identityRegionsOnce sync.Once
	identityRegionsErr  error
)

func checkRegionsFromIdentity(provider common.ConfigurationProvider, candidate string) (string, bool) {
	identityRegionsOnce.Do(func() {
		identityRegionsErr = populateIdentityRegions(provider)
	})
	if identityRegionsErr != nil {
		debugf("Identity regions lookup failed: %v", identityRegionsErr)
		return "", false
	}
	if _, ok := identityRegions[candidate]; ok {
		debugf("Candidate %s matched tenant Identity regions", candidate)
		return candidate, true
	}
	return "", false
}

func populateIdentityRegions(provider common.ConfigurationProvider) error {
	client, err := identity.NewIdentityClientWithConfigurationProvider(provider)
	if err != nil {
		return err
	}
	resp, err := client.ListRegions(context.Background())
	if err != nil {
		return err
	}
	for _, region := range resp.Items {
		if region.Name != nil {
			debugf("Discovered tenant region: %s", *region.Name)
			identityRegions[strings.ToLower(*region.Name)] = struct{}{}
		}
	}
	return nil
}

func debugf(format string, args ...interface{}) {
	if !debugEnabled {
		return
	}
	fmt.Fprintf(os.Stderr, "[debug] "+format+"\n", args...)
}
