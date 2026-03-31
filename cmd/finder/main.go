package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	tokenpool "github.com/flynnkc/token-pool"
	common "github.com/oracle/oci-go-sdk/v65/common"
	identity "github.com/oracle/oci-go-sdk/v65/identity"
	search "github.com/oracle/oci-go-sdk/v65/resourcesearch"
)

type queryResult struct {
	OCID      string                   `json:"ocid"`
	Region    string                   `json:"region,omitempty"`
	Resources []search.ResourceSummary `json:"resources,omitempty"`
	Message   string                   `json:"message,omitempty"`
}

type resourceSearchClient interface {
	SetRegion(string)
	SearchResources(context.Context, search.SearchResourcesRequest) (search.SearchResourcesResponse, error)
}

var (
	debugEnabled bool
	logger       = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "OCI Structured Search CLI\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [--resource-type <type>] [--region <region>] [--profile <profile>] <resource-id> [<resource-id> ...]\n\n", os.Args[0])
		flag.PrintDefaults()
	}

	region := flag.String("region", "", "OCI region to target (defaults to profile region)")
	profile := flag.String("profile", "DEFAULT", "OCI CLI profile name from ~/.oci/config")
	resourceType := flag.String("resource-type", "all", "OCI resource type to search (defaults to all)")
	workerCount := flag.Int("concurrency", 5, "Maximum number of concurrent OCI search requests")
	rateLimit := flag.Int("rate-limit", 5, "Number of requests replenished every rate interval")
	rateInterval := flag.Duration("rate-interval", time.Second, "Interval for rate-limit replenishment (e.g. 1s, 500ms)")
	rateBurst := flag.Int("rate-burst", 5, "Maximum burst size for rate limiting (defaults to rate-limit)")
	flag.BoolVar(&debugEnabled, "debug", false, "Enable verbose debug logging")

	flag.Parse()

	if debugEnabled {
		handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
		logger = slog.New(handler)
		logger.Debug("debug logging enabled")
	}

	resourceIDs := normalizeResourceIDs(flag.Args())
	if len(resourceIDs) == 0 {
		flag.Usage()
		fmt.Fprintln(os.Stderr, "error: at least one resource-id positional argument is required")
		os.Exit(1)
	}

	if err := run(*region, *profile, *resourceType, resourceIDs, *workerCount, *rateLimit, *rateBurst, *rateInterval); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func normalizeResourceIDs(rawArgs []string) []string {
	if len(rawArgs) == 0 {
		return nil
	}
	replacer := strings.NewReplacer("\"", "", "'", "")
	joined := strings.Join(rawArgs, "\n")
	cleaned := replacer.Replace(joined)
	fields := strings.Fields(cleaned)
	logger.Debug("normalized resource identifiers", slog.Int("inputArgs", len(rawArgs)), slog.Int("normalized", len(fields)))
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func run(region, profile, resourceType string, resourceIDs []string, workerCount, rateLimit, rateBurst int, rateInterval time.Duration) error {
	configProvider, err := configurationProvider(profile)
	if err != nil {
		return fmt.Errorf("load OCI config: %w", err)
	}

	client, err := search.NewResourceSearchClientWithConfigurationProvider(configProvider)
	if err != nil {
		return fmt.Errorf("create resource search client: %w", err)
	}

	resourceScope := resourceType
	if resourceScope == "" {
		resourceScope = "all"
	}

	workerCount = effectiveWorkerCount(workerCount, len(resourceIDs))
	if rateBurst <= 0 {
		rateBurst = rateLimit
	}
	if rateBurst <= 0 {
		rateBurst = workerCount
	}
	if rateLimit <= 0 {
		rateLimit = workerCount
	}
	if rateInterval <= 0 {
		rateInterval = time.Second
	}

	logger.Debug("runtime configuration", slog.String("resourceScope", resourceScope), slog.Int("workerCount", workerCount), slog.Int("rateLimit", rateLimit), slog.Int("rateBurst", rateBurst), slog.Duration("rateInterval", rateInterval), slog.Int("resourceIDs", len(resourceIDs)))

	pool := tokenpool.NewTokenPool(rateBurst, rateLimit, rateInterval)
	defer pool.Close()
	acquire := func(ctx context.Context) bool {
		return pool.Acquire(ctx)
	}
	logger.Debug("token pool initialized", slog.Int("burst", rateBurst), slog.Int("limit", rateLimit))

	ctx, cancel := signalContext()
	defer cancel()

	if len(resourceIDs) == 1 {
		logger.Debug("single resource optimization", slog.String("ocid", resourceIDs[0]))
		result := processResourceID(ctx, &client, configProvider, region, resourceScope, resourceIDs[0], acquire)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode([]queryResult{result})
	}

	type job struct {
		index int
		ocid  string
	}

	jobs := make(chan job)
	results := make([]queryResult, len(resourceIDs))
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		workerID := i
		logger.Debug("starting worker", slog.Int("worker", workerID))
		go func(id int) {
			defer wg.Done()
			for j := range jobs {
				logger.Debug("worker received job", slog.Int("worker", id), slog.Int("index", j.index), slog.String("ocid", j.ocid))
				res := processResourceID(ctx, &client, configProvider, region, resourceScope, j.ocid, acquire)
				results[j.index] = res
				logger.Debug("worker completed job", slog.Int("worker", id), slog.Int("index", j.index), slog.String("ocid", j.ocid), slog.String("message", res.Message), slog.Int("resultCount", len(res.Resources)))
			}
		}(workerID)
	}

	for idx, ocid := range resourceIDs {
		logger.Debug("enqueue resource", slog.Int("index", idx), slog.String("ocid", ocid))
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- job{index: idx, ocid: ocid}:
		}
	}
	close(jobs)
	logger.Debug("all jobs dispatched", slog.Int("count", len(resourceIDs)))
	wg.Wait()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

func processResourceID(parentCtx context.Context, client resourceSearchClient, provider common.ConfigurationProvider, flagRegion, resourceScope, resourceID string, acquire func(context.Context) bool) queryResult {
	result := queryResult{OCID: resourceID}

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	if acquire != nil {
		logger.Debug("attempting to acquire rate token", slog.String("ocid", resourceID))
		if !acquire(ctx) {
			result.Message = "rate limit not available"
			logger.Debug("rate limit not available", slog.String("ocid", resourceID))
			return result
		}
	}

	regionToUse, err := resolveRegion(flagRegion, resourceID, provider)
	if err != nil {
		result.Message = fmt.Sprintf("resolve region: %v", err)
		logger.Debug("failed to resolve region", slog.String("ocid", resourceID), slog.Any("error", err))
		return result
	}

	result.Region = regionToUse
	logger.Debug("resolved region", slog.String("ocid", resourceID), slog.String("region", regionToUse))
	client.SetRegion(regionToUse)

	query := fmt.Sprintf("query %s resources where identifier = '%s'", resourceScope, resourceID)
	request := search.SearchResourcesRequest{
		SearchDetails: search.StructuredSearchDetails{
			MatchingContextType: search.SearchDetailsMatchingContextTypeHighlights,
			Query:               common.String(query),
		},
		Limit: common.Int(25),
	}

	ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	logger.Debug("issuing structured search", slog.String("ocid", resourceID), slog.String("region", regionToUse), slog.String("query", query))
	response, err := client.SearchResources(ctx, request)
	if err != nil {
		result.Message = fmt.Sprintf("search resources: %v", err)
		logger.Debug("search request failed", slog.String("ocid", resourceID), slog.String("region", regionToUse), slog.Any("error", err))
		return result
	}

	if len(response.Items) == 0 {
		result.Message = "No resources found matching the criteria."
		logger.Debug("search returned no results", slog.String("ocid", resourceID), slog.String("region", regionToUse))
		return result
	}

	result.Resources = response.Items
	logger.Debug("search returned results", slog.String("ocid", resourceID), slog.String("region", regionToUse), slog.Int("count", len(response.Items)))
	return result
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
		logger.Debug("region provided via flag", slog.String("region", flagRegion))
		return flagRegion, nil
	}

	if ocidRegion, ok := regionFromOCID(resourceID, provider); ok {
		logger.Debug("region derived from OCID", slog.String("ocid", resourceID), slog.String("region", ocidRegion))
		return ocidRegion, nil
	}

	profileRegion, err := provider.Region()
	if err != nil {
		return "", fmt.Errorf("determine region from profile: %w", err)
	}
	logger.Debug("falling back to profile region", slog.String("region", profileRegion))
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
var shortRegionAliases = map[string]string{
	"iad": "us-ashburn-1",
	"phx": "us-phoenix-1",
	"sjc": "us-sanjose-1",
	"chi": "us-chicago-1",
	"aus": "us-austin-1",
}

func regionFromOCID(ocid string, provider common.ConfigurationProvider) (string, bool) {
	logger.Debug("attempting to derive region from OCID", slog.String("ocid", ocid))
	parts := strings.Split(ocid, ".")
	for _, part := range parts {
		candidate := strings.ToLower(part)
		if regionTokenRegexp.MatchString(candidate) {
			logger.Debug("evaluating candidate region token", slog.String("token", candidate))
			if _, ok := knownRegions[candidate]; ok {
				return candidate, true
			}
			logger.Debug("token not in known regions; checking identity API", slog.String("token", candidate))
			if regionFromAPI, ok := checkRegionsFromIdentity(provider, candidate); ok {
				return regionFromAPI, true
			}
			continue
		}

		if full, ok := shortRegionAliases[candidate]; ok {
			logger.Debug("translated short region code", slog.String("short", candidate), slog.String("region", full))
			if _, exists := knownRegions[full]; exists {
				return full, true
			}
			if regionFromAPI, ok := checkRegionsFromIdentity(provider, full); ok {
				return regionFromAPI, true
			}
			continue
		}

		logger.Debug("skipping token (not region-like)", slog.String("token", candidate))
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
		logger.Debug("identity regions lookup failed", slog.Any("error", identityRegionsErr))
		return "", false
	}
	if _, ok := identityRegions[candidate]; ok {
		logger.Debug("candidate matched tenant identity regions", slog.String("candidate", candidate))
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
			logger.Debug("discovered tenant region", slog.String("region", *region.Name))
			identityRegions[strings.ToLower(*region.Name)] = struct{}{}
		}
	}
	return nil
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(sigCh)
		close(sigCh)
	}()
	return ctx, cancel
}

func effectiveWorkerCount(requested, total int) int {
	if requested <= 0 {
		requested = 1
	}
	if total <= 0 {
		return requested
	}
	if requested > total {
		return total
	}
	return requested
}
