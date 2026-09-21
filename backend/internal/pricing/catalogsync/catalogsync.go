// Package catalogsync builds the reviewed, content-addressed pricing catalog
// from LiteLLM's model price source. It deliberately has no network dependency
// so the command and its tests can supply a pinned source file directly.
package catalogsync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	catalogRelativePath = "pricing/catalog/v1"
	schemaVersion       = 1
	maxCanonicalRateLen = 128
)

// Source identifies the immutable upstream file from which a catalog was
// generated.
type Source struct {
	Repository string
	Revision   string
	Path       string
}

// Result reports whether Sync changed the reviewed catalog.
type Result struct {
	Changed bool
}

type manifest struct {
	SchemaVersion int            `json:"schemaVersion"`
	Source        manifestSource `json:"source"`
	Providers     []providerRef  `json:"providers"`
}

type manifestSource struct {
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Path       string `json:"path"`
}

type providerRef struct {
	ProviderID string `json:"providerId"`
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
	Path       string `json:"path"`
	ModelCount int    `json:"modelCount"`
}

type providerBlob struct {
	SchemaVersion int           `json:"schemaVersion"`
	ProviderID    string        `json:"providerId"`
	Models        []pricedModel `json:"models"`
}

type pricedModel struct {
	ModelID string `json:"modelId"`
	Rates   rates  `json:"rates"`
}

type rates struct {
	UncachedInputUSDPerToken string  `json:"uncachedInputUsdPerToken"`
	CacheReadUSDPerToken     *string `json:"cacheReadUsdPerToken,omitempty"`
	CacheWriteUSDPerToken    *string `json:"cacheWriteUsdPerToken,omitempty"`
	CacheWrite1HUSDPerToken  *string `json:"cacheWrite1hUsdPerToken,omitempty"`
	OutputUSDPerToken        string  `json:"outputUsdPerToken"`
}

type upstreamRecord struct {
	Provider string `json:"litellm_provider"`
	Mode     string `json:"mode"`
	Input    *json.Number
	Output   *json.Number
	Read     *json.Number
	Write    *json.Number
	Write1H  *json.Number
}

func (u *upstreamRecord) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	if err := decodeString(fields["litellm_provider"], &u.Provider); err != nil {
		return fmt.Errorf("litellm_provider: %w", err)
	}
	if err := decodeString(fields["mode"], &u.Mode); err != nil {
		return fmt.Errorf("mode: %w", err)
	}
	var err error
	if u.Input, err = decodeNumber(fields["input_cost_per_token"]); err != nil {
		return fmt.Errorf("input_cost_per_token: %w", err)
	}
	if u.Output, err = decodeNumber(fields["output_cost_per_token"]); err != nil {
		return fmt.Errorf("output_cost_per_token: %w", err)
	}
	if u.Read, err = decodeNumber(fields["cache_read_input_token_cost"]); err != nil {
		return fmt.Errorf("cache_read_input_token_cost: %w", err)
	}
	if u.Write, err = decodeNumber(fields["cache_creation_input_token_cost"]); err != nil {
		return fmt.Errorf("cache_creation_input_token_cost: %w", err)
	}
	if u.Write1H, err = decodeNumber(fields["cache_creation_input_token_cost_above_1hr"]); err != nil {
		return fmt.Errorf("cache_creation_input_token_cost_above_1hr: %w", err)
	}
	return nil
}

func decodeString(raw json.RawMessage, target *string) error {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	return json.Unmarshal(raw, target)
}

func decodeNumber(raw json.RawMessage) (*json.Number, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var number json.Number
	if err := decoder.Decode(&number); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("must be one JSON number")
	}
	return &number, nil
}

// Sync generates a catalog under root. Existing, hash-addressed provider blobs
// are never modified or removed. A source revision change with identical
// provider payload hashes is a semantic no-op and leaves the manifest intact.
func Sync(root string, upstream []byte, source Source) (Result, error) {
	generated, err := build(upstream, source)
	if err != nil {
		return Result{}, err
	}
	dir := filepath.Join(root, filepath.FromSlash(catalogRelativePath))
	current, err := readManifest(filepath.Join(dir, "manifest.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	if err == nil && sameProviders(current.Providers, generated.manifest.Providers) {
		return Result{}, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Result{}, fmt.Errorf("create catalog directory: %w", err)
	}
	for providerID, blob := range generated.blobs {
		ref := findProvider(generated.manifest.Providers, providerID)
		path := filepath.Join(dir, filepath.FromSlash(ref.Path))
		if err := writeAppendOnly(path, blob); err != nil {
			return Result{}, err
		}
	}
	manifestBytes, err := canonicalJSON(generated.manifest)
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestBytes, 0o600); err != nil {
		return Result{}, fmt.Errorf("write manifest: %w", err)
	}
	return Result{Changed: true}, nil
}

// Validate verifies the manifest and every provider blob it references. It
// intentionally permits unreferenced historical blobs in the provider folders.
func Validate(root string) error {
	dir := filepath.Join(root, filepath.FromSlash(catalogRelativePath))
	m, err := readManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return err
	}
	if err := validateManifest(m); err != nil {
		return err
	}
	for _, ref := range m.Providers {
		if !isProviderPath(ref.ProviderID, ref.SHA256, ref.Path) {
			return fmt.Errorf("provider %q has unsafe or noncanonical path %q", ref.ProviderID, ref.Path)
		}
		contents, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ref.Path)))
		if err != nil {
			return fmt.Errorf("read provider %q: %w", ref.ProviderID, err)
		}
		if hash(contents) != ref.SHA256 {
			return fmt.Errorf("provider %q hash mismatch", ref.ProviderID)
		}
		var blob providerBlob
		if err := json.Unmarshal(contents, &blob); err != nil {
			return fmt.Errorf("decode provider %q: %w", ref.ProviderID, err)
		}
		canonical, err := canonicalJSON(blob)
		if err != nil || !bytes.Equal(contents, canonical) {
			return fmt.Errorf("provider %q is not canonical JSON", ref.ProviderID)
		}
		if err := validateBlob(blob, ref); err != nil {
			return err
		}
	}
	return nil
}

type generatedCatalog struct {
	manifest manifest
	blobs    map[string][]byte
}

func build(upstream []byte, source Source) (generatedCatalog, error) {
	if source.Repository != "BerriAI/litellm" || source.Path != "model_prices_and_context_window.json" || !isSHA(source.Revision) {
		return generatedCatalog{}, errors.New("source must identify BerriAI/litellm at an exact 40-character revision")
	}
	decoder := json.NewDecoder(bytes.NewReader(upstream))
	decoder.UseNumber()
	var records map[string]upstreamRecord
	if err := decoder.Decode(&records); err != nil {
		return generatedCatalog{}, fmt.Errorf("decode upstream catalog: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return generatedCatalog{}, errors.New("upstream catalog must contain one JSON object")
	}
	modelsByProvider := map[string]map[string]rates{
		"anthropic": {}, "openai": {}, "zai": {},
	}
	for name, record := range records {
		providerID, ok := canonicalProvider(record.Provider)
		if !ok || !supportedMode(record.Mode) {
			continue
		}
		// LiteLLM also carries metadata-only model entries in chat/responses
		// modes. They are not price records and cannot enter a reviewed catalog.
		if record.Input == nil || record.Output == nil {
			continue
		}
		modelID, err := canonicalModel(providerID, name)
		if err != nil {
			return generatedCatalog{}, err
		}
		r, err := normalizeRates(record)
		if err != nil {
			return generatedCatalog{}, fmt.Errorf("%s/%s: %w", providerID, modelID, err)
		}
		if previous, exists := modelsByProvider[providerID][modelID]; exists && !equalRates(previous, r) {
			return generatedCatalog{}, fmt.Errorf("%s/%s: conflicting duplicate rates", providerID, modelID)
		}
		modelsByProvider[providerID][modelID] = r
	}

	providers := []string{"anthropic", "openai", "zai"}
	generated := generatedCatalog{blobs: make(map[string][]byte)}
	generated.manifest = manifest{SchemaVersion: schemaVersion, Source: manifestSource(source), Providers: make([]providerRef, 0, len(providers))}
	for _, providerID := range providers {
		models := modelsByProvider[providerID]
		if len(models) == 0 {
			return generatedCatalog{}, fmt.Errorf("provider %q produced zero supported models", providerID)
		}
		ids := make([]string, 0, len(models))
		for modelID := range models {
			ids = append(ids, modelID)
		}
		sort.Strings(ids)
		blob := providerBlob{SchemaVersion: schemaVersion, ProviderID: providerID, Models: make([]pricedModel, 0, len(ids))}
		for _, modelID := range ids {
			blob.Models = append(blob.Models, pricedModel{
				ModelID: modelID,
				Rates:   withPlausibleCacheTiers(providerID, models[modelID]),
			})
		}
		contents, err := canonicalJSON(blob)
		if err != nil {
			return generatedCatalog{}, err
		}
		digest := hash(contents)
		generated.blobs[providerID] = contents
		generated.manifest.Providers = append(generated.manifest.Providers, providerRef{
			ProviderID: providerID,
			Version:    "ao-catalog:" + providerID + ":sha256:" + digest,
			SHA256:     digest,
			Path:       "providers/" + providerID + "/" + digest + ".json",
			ModelCount: len(blob.Models),
		})
	}
	return generated, nil
}

func normalizeRates(record upstreamRecord) (rates, error) {
	input, err := normalizeRequired(record.Input)
	if err != nil {
		return rates{}, fmt.Errorf("input: %w", err)
	}
	output, err := normalizeRequired(record.Output)
	if err != nil {
		return rates{}, fmt.Errorf("output: %w", err)
	}
	read, err := normalizeOptional(record.Read)
	if err != nil {
		return rates{}, fmt.Errorf("cache read: %w", err)
	}
	write, err := normalizeOptional(record.Write)
	if err != nil {
		return rates{}, fmt.Errorf("cache write: %w", err)
	}
	write1H, err := normalizeOptional(record.Write1H)
	if err != nil {
		return rates{}, fmt.Errorf("1h cache write: %w", err)
	}
	return rates{input, read, write, write1H, output}, nil
}

func normalizeRequired(number *json.Number) (string, error) {
	if number == nil {
		return "", errors.New("rate is required")
	}
	return normalizeDecimal(number.String())
}

func normalizeOptional(number *json.Number) (*string, error) {
	if number == nil {
		return nil, nil
	}
	value, err := normalizeDecimal(number.String())
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// normalizeDecimal parses a JSON decimal or scientific notation without a
// float64 round trip and returns its canonical non-exponent decimal spelling.
func normalizeDecimal(input string) (string, error) {
	if input == "" {
		return "", errors.New("rate is not a number")
	}
	if strings.HasPrefix(input, "-") {
		return "", errors.New("rate must be nonnegative")
	}
	if strings.HasPrefix(input, "+") {
		return "", errors.New("rate is not valid JSON number")
	}
	parts := strings.FieldsFunc(input, func(r rune) bool { return r == 'e' || r == 'E' })
	if len(parts) > 2 || len(parts) == 0 {
		return "", errors.New("rate is not a decimal")
	}
	mantissa := parts[0]
	exponent := 0
	if len(parts) == 2 {
		parsed, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return "", errors.New("rate has invalid exponent")
		}
		if parsed < -maxCanonicalRateLen || parsed > maxCanonicalRateLen {
			return "", errors.New("rate exponent is out of range")
		}
		exponent = int(parsed)
	}
	decimal := strings.Split(mantissa, ".")
	if len(decimal) > 2 || len(decimal) == 0 || (len(decimal) == 2 && decimal[1] == "") {
		return "", errors.New("rate is not a decimal")
	}
	digits := strings.Join(decimal, "")
	if digits == "" {
		return "", errors.New("rate is not a decimal")
	}
	if len(digits) > maxCanonicalRateLen {
		return "", errors.New("rate exceeds supported precision")
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return "", errors.New("rate is not a decimal")
		}
	}
	fraction := 0
	if len(decimal) == 2 {
		fraction = len(decimal[1])
	}
	shift := exponent - fraction
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0", nil
	}
	if shift >= 0 {
		if shift > maxCanonicalRateLen-len(digits) {
			return "", errors.New("rate exceeds supported precision")
		}
		return digits + strings.Repeat("0", shift), nil
	}
	point := len(digits) + shift
	if point <= 0 {
		zeroes := -point
		if len(digits) > maxCanonicalRateLen-2 || zeroes > maxCanonicalRateLen-2-len(digits) {
			return "", errors.New("rate exceeds supported precision")
		}
		result := "0." + strings.Repeat("0", zeroes) + digits
		return strings.TrimRight(result, "0"), nil
	}
	if len(digits) >= maxCanonicalRateLen {
		return "", errors.New("rate exceeds supported precision")
	}
	result := digits[:point] + "." + digits[point:]
	result = strings.TrimRight(result, "0")
	return strings.TrimSuffix(result, "."), nil
}

func canonicalProvider(value string) (string, bool) {
	provider := strings.ToLower(strings.TrimSpace(value))
	if provider == "z.ai" {
		provider = "zai"
	}
	switch provider {
	case "anthropic", "openai", "zai":
		return provider, true
	default:
		return "", false
	}
}

func canonicalModel(providerID, value string) (string, error) {
	modelID := strings.ToLower(strings.TrimSpace(value))
	modelID = strings.TrimPrefix(modelID, providerID+"/")
	if modelID == "" {
		return "", fmt.Errorf("%s: empty model identifier", providerID)
	}
	return modelID, nil
}

func supportedMode(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "chat", "responses":
		return true
	default:
		return false
	}
}

func canonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readManifest(path string) (manifest, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(contents, &m); err != nil {
		return manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	canonical, err := canonicalJSON(m)
	if err != nil || !bytes.Equal(contents, canonical) {
		return manifest{}, errors.New("manifest is not canonical JSON")
	}
	return m, nil
}

func writeAppendOnly(path string, contents []byte) (err error) {
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, contents) {
			return fmt.Errorf("refusing to modify existing provider blob %s", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return writeAppendOnly(path, contents)
		}
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	return nil
}

func sameProviders(left, right []providerRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func findProvider(providers []providerRef, providerID string) providerRef {
	for _, provider := range providers {
		if provider.ProviderID == providerID {
			return provider
		}
	}
	panic("missing generated provider " + providerID)
}

func equalRates(left, right rates) bool {
	return left.UncachedInputUSDPerToken == right.UncachedInputUSDPerToken &&
		equalOptionalRate(left.CacheReadUSDPerToken, right.CacheReadUSDPerToken) &&
		equalOptionalRate(left.CacheWriteUSDPerToken, right.CacheWriteUSDPerToken) &&
		equalOptionalRate(left.CacheWrite1HUSDPerToken, right.CacheWrite1HUSDPerToken) &&
		left.OutputUSDPerToken == right.OutputUSDPerToken
}

func equalOptionalRate(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func isSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isProviderPath(providerID, digest, path string) bool {
	return path == "providers/"+providerID+"/"+digest+".json" && isSHA256(digest)
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateManifest(m manifest) error {
	if m.SchemaVersion != schemaVersion || m.Source.Repository != "BerriAI/litellm" || m.Source.Path != "model_prices_and_context_window.json" || !isSHA(m.Source.Revision) {
		return errors.New("invalid manifest schema or source")
	}
	if len(m.Providers) != 3 {
		return errors.New("manifest must contain exactly three providers")
	}
	for index, providerID := range []string{"anthropic", "openai", "zai"} {
		ref := m.Providers[index]
		if ref.ProviderID != providerID || ref.ModelCount < 1 || ref.Version != "ao-catalog:"+providerID+":sha256:"+ref.SHA256 || !isProviderPath(providerID, ref.SHA256, ref.Path) {
			return fmt.Errorf("invalid manifest provider %q", providerID)
		}
	}
	return nil
}

// withPlausibleCacheTiers drops an Anthropic cache-write rate the upstream feed
// cannot mean, rather than shipping it.
//
// Anthropic prices a 5-minute write above base input and a 1-hour write above
// that; the feed has carried both a 1-hour rate below the 5-minute rate
// (claude-3-opus-20240229, 5x too low) and one twenty-four times base input
// (claude-3-haiku-20240307, 12x too high) — in both cases the value belonging
// to a different model. An absent rate prices that bucket as unknown, which the
// estimator already reports honestly; a wrong one is billed as fact.
//
// This is deliberately not applied to the other providers, whose tiers do not
// follow that shape: z.ai writes to cache for free, and an OpenAI fine-tune
// prices a write at half its input rate. Both are real, and a relational
// invariant borrowed from Anthropic would delete them.
func withPlausibleCacheTiers(providerID string, r rates) rates {
	if providerID != "anthropic" {
		return r
	}
	if err := checkAnthropicCacheTiers(r); err == nil {
		return r
	}
	trimmed := r
	trimmed.CacheWrite1HUSDPerToken = nil
	if checkAnthropicCacheTiers(trimmed) == nil {
		return trimmed
	}
	trimmed.CacheWriteUSDPerToken = nil
	return trimmed
}

// maxAnthropicWriteMultiple bounds a 1-hour write against base input. Anthropic
// documents 2x; the headroom leaves room for a repricing without letting a
// value from another model's row through.
var maxAnthropicWriteMultiple = big.NewRat(5, 2)

// checkAnthropicCacheTiers reports the relationship Anthropic's published
// pricing keeps: a write is never cheaper than plain input, a longer time to
// live is never cheaper than a shorter one, and an hour is not an order of
// magnitude above base input. The exact multiples are not asserted, because
// Anthropic rounds them — claude-3-haiku's 5-minute write is 1.2x base input,
// not the documented 1.25x.
func checkAnthropicCacheTiers(r rates) error {
	input, ok := new(big.Rat).SetString(r.UncachedInputUSDPerToken)
	if !ok {
		return fmt.Errorf("uninterpretable input rate %q", r.UncachedInputUSDPerToken)
	}
	write, err := optionalRat(r.CacheWriteUSDPerToken)
	if err != nil {
		return err
	}
	write1H, err := optionalRat(r.CacheWrite1HUSDPerToken)
	if err != nil {
		return err
	}
	if write != nil && write.Cmp(input) < 0 {
		return fmt.Errorf("5m write %s is below input %s", write.RatString(), input.RatString())
	}
	if write1H != nil {
		if write != nil && write1H.Cmp(write) < 0 {
			return fmt.Errorf("1h write %s is below the 5m write %s", write1H.RatString(), write.RatString())
		}
		if ceiling := new(big.Rat).Mul(input, maxAnthropicWriteMultiple); write1H.Cmp(ceiling) > 0 {
			return fmt.Errorf("1h write %s exceeds %s times input %s",
				write1H.RatString(), maxAnthropicWriteMultiple.RatString(), input.RatString())
		}
	}
	return nil
}

func optionalRat(value *string) (*big.Rat, error) {
	if value == nil {
		return nil, nil
	}
	parsed, ok := new(big.Rat).SetString(*value)
	if !ok {
		return nil, fmt.Errorf("uninterpretable rate %q", *value)
	}
	return parsed, nil
}

func validateBlob(blob providerBlob, ref providerRef) error {
	if blob.SchemaVersion != schemaVersion || blob.ProviderID != ref.ProviderID || len(blob.Models) != ref.ModelCount {
		return fmt.Errorf("invalid provider blob %q", ref.ProviderID)
	}
	previous := ""
	for _, model := range blob.Models {
		if model.ModelID == "" || model.ModelID != strings.ToLower(strings.TrimSpace(model.ModelID)) || model.ModelID <= previous {
			return fmt.Errorf("provider %q has unsorted or noncanonical models", ref.ProviderID)
		}
		previous = model.ModelID
		if err := validateCanonicalRate(model.Rates.UncachedInputUSDPerToken); err != nil {
			return fmt.Errorf("provider %q invalid input rate: %w", ref.ProviderID, err)
		}
		if err := validateCanonicalRate(model.Rates.OutputUSDPerToken); err != nil {
			return fmt.Errorf("provider %q invalid output rate: %w", ref.ProviderID, err)
		}
		for _, optional := range []*string{model.Rates.CacheReadUSDPerToken, model.Rates.CacheWriteUSDPerToken, model.Rates.CacheWrite1HUSDPerToken} {
			if optional != nil {
				if err := validateCanonicalRate(*optional); err != nil {
					return fmt.Errorf("provider %q invalid cache rate: %w", ref.ProviderID, err)
				}
			}
		}
		// Canonical spelling says nothing about whether a number is possible.
		// A rate the provider cannot mean is billed as confidently as a right
		// one, so the relationship is checked on everything that ships.
		if ref.ProviderID == "anthropic" {
			if err := checkAnthropicCacheTiers(model.Rates); err != nil {
				return fmt.Errorf("provider %q model %q implausible cache tiers: %w", ref.ProviderID, model.ModelID, err)
			}
		}
	}
	return nil
}

func validateCanonicalRate(value string) error {
	normalized, err := normalizeDecimal(value)
	if err != nil {
		return err
	}
	if normalized != value {
		return fmt.Errorf("noncanonical rate %q", value)
	}
	return nil
}
