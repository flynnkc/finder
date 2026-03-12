package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	common "github.com/oracle/oci-go-sdk/v65/common"
	search "github.com/oracle/oci-go-sdk/v65/resourcesearch"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "OCI Structured Search CLI\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [--resource-type <type>] [--region <region>] [--profile <profile>] <resource-id>\n\n", os.Args[0])
		flag.PrintDefaults()
	}

	region := flag.String("region", "", "OCI region to target (defaults to profile region)")
	profile := flag.String("profile", "DEFAULT", "OCI CLI profile name from ~/.oci/config")
	resourceType := flag.String("resource-type", "all", "OCI resource type to search (defaults to all)")

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

	regionToUse := region
	if regionToUse == "" {
		profileRegion, err := configProvider.Region()
		if err != nil {
			return fmt.Errorf("determine region from profile: %w", err)
		}
		regionToUse = profileRegion
	}

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
