// Package catalog defines the reviewed list of model bundles shipped with the app.
package catalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	SchemaVersion     = 1
	MaxArtifacts      = 256
	maxPathComponents = 8
)

const (
	EngineUnknown     = "unknown"
	EngineConfirmed   = "confirmed"
	EngineUnsupported = "unsupported"

	ReviewUnreviewed = "unreviewed"
	ReviewApproved   = "approved"
	ReviewBlocked    = "blocked"
)

var (
	ErrUnsupportedSchema = errors.New("unsupported catalog schema")
	ErrUnsafePath        = errors.New("unsafe artifact path")

	idPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,95}$`)
	revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	repoPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	componentRunes  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

//go:embed catalog.json
var embedded []byte

// Descriptor is the reviewed identity of one exact downloadable selection.
// Every field is covered by the manifest digest.
type Descriptor struct {
	SchemaVersion  int               `json:"schemaVersion"`
	CatalogVersion string            `json:"catalogVersion"`
	ModelID        string            `json:"modelId"`
	VariantID      string            `json:"variantId"`
	BundleID       string            `json:"bundleId"`
	VoiceIDs       []string          `json:"voiceIds"`
	DisplayName    string            `json:"displayName"`
	Publisher      string            `json:"publisher"`
	Description    string            `json:"description"`
	SourceURL      string            `json:"sourceUrl"`
	ModelCardURL   string            `json:"modelCardUrl"`
	ReleaseLabel   string            `json:"releaseLabel"`
	Languages      []string          `json:"languages"`
	Speakers       *int              `json:"speakers"`
	SampleRateHz   *int              `json:"sampleRateHz"`
	Format         string            `json:"format"`
	Precision      string            `json:"precision"`
	Capabilities   []string          `json:"capabilities"`
	Engine         Engine            `json:"engine"`
	Artifacts      []Artifact        `json:"artifacts"`
	Licensing      []LicenseEvidence `json:"licensing"`
	Space          Space             `json:"space"`
}

type Engine struct {
	Status      string   `json:"status"`
	Evidence    *string  `json:"evidence"`
	Constraints []string `json:"constraints"`
}

type Artifact struct {
	ArtifactID     string  `json:"artifactId"`
	Repository     string  `json:"repository"`
	Revision       string  `json:"revision"`
	Path           string  `json:"path"`
	URL            string  `json:"url"`
	SizeBytes      *int64  `json:"sizeBytes"`
	SHA256         string  `json:"sha256"`
	HashSource     string  `json:"hashSource"`
	HashObservedAt string  `json:"hashObservedAt"`
	VoiceID        *string `json:"voiceId"`
}

// Size returns the validated byte count.
func (a Artifact) Size() int64 {
	if a.SizeBytes == nil {
		return 0
	}
	return *a.SizeBytes
}

type LicenseEvidence struct {
	Kind               string  `json:"kind"`
	Subject            string  `json:"subject"`
	SourceURL          string  `json:"sourceUrl"`
	SourceRevision     string  `json:"sourceRevision"`
	LicenseID          *string `json:"licenseId"`
	Attribution        string  `json:"attribution"`
	AcceptanceRequired bool    `json:"acceptanceRequired"`
	DistributionReview string  `json:"distributionReview"`
}

type Space struct {
	RequiredBytes        int64  `json:"requiredBytes"`
	InstalledBytes       int64  `json:"installedBytes"`
	StagingHeadroomBytes int64  `json:"stagingHeadroomBytes"`
	CopyHeadroomBytes    int64  `json:"copyHeadroomBytes"`
	MemoryBytes          *int64 `json:"memoryBytes"`
}

// Bundle is a validated descriptor and its content identity.
type Bundle struct {
	Descriptor
	ManifestSHA256 string
}

// Eligibility states whether a bundle may be offered for download.
type Eligibility struct {
	Eligible bool
	Reason   string
}

// DownloadEligibility requires complete, approved license evidence. No
// acceptance flow exists, so bundles that require one stay unavailable.
func (b Bundle) DownloadEligibility() Eligibility {
	hasModel := false
	for _, evidence := range b.Licensing {
		hasModel = hasModel || evidence.Kind == "model"
		switch {
		case evidence.DistributionReview == ReviewBlocked:
			return Eligibility{Reason: "Distribution of this model is blocked."}
		case evidence.DistributionReview != ReviewApproved:
			return Eligibility{Reason: "License review is not complete."}
		case evidence.LicenseID == nil:
			return Eligibility{Reason: "License terms are unresolved."}
		case evidence.AcceptanceRequired:
			return Eligibility{Reason: "This model requires accepting license terms."}
		}
	}
	if !hasModel {
		return Eligibility{Reason: "License evidence is incomplete."}
	}
	return Eligibility{Eligible: true}
}

// Catalog is an ordered set of validated bundles.
type Catalog struct {
	Bundles []Bundle
}

// Find returns the bundle with the given ID.
func (c *Catalog) Find(bundleID string) (Bundle, bool) {
	for _, bundle := range c.Bundles {
		if bundle.BundleID == bundleID {
			return bundle, true
		}
	}
	return Bundle{}, false
}

// Load parses the catalog embedded in this build.
func Load() (*Catalog, error) {
	return Parse(embedded)
}

type file struct {
	SchemaVersion int          `json:"schemaVersion"`
	Descriptors   []Descriptor `json:"descriptors"`
}

// Parse strictly decodes and validates a catalog document.
func Parse(data []byte) (*Catalog, error) {
	var header struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if header.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("catalog: %w: %d", ErrUnsupportedSchema, header.SchemaVersion)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var parsed file
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("catalog: trailing data after document")
	}
	catalog := &Catalog{}
	seen := map[string]bool{}
	for _, descriptor := range parsed.Descriptors {
		bundle, err := NewBundle(descriptor)
		if err != nil {
			return nil, err
		}
		if seen[bundle.BundleID] {
			return nil, fmt.Errorf("catalog: duplicate bundle %q", bundle.BundleID)
		}
		seen[bundle.BundleID] = true
		catalog.Bundles = append(catalog.Bundles, bundle)
	}
	return catalog, nil
}

// NewBundle validates a descriptor, normalizes its order, and computes its digest.
func NewBundle(descriptor Descriptor) (Bundle, error) {
	descriptor.VoiceIDs = slices.Sorted(slices.Values(descriptor.VoiceIDs))
	descriptor.Artifacts = slices.Clone(descriptor.Artifacts)
	slices.SortFunc(descriptor.Artifacts, func(a, b Artifact) int { return strings.Compare(a.Path, b.Path) })
	if err := validate(descriptor); err != nil {
		return Bundle{}, fmt.Errorf("catalog: bundle %q: %w", descriptor.BundleID, err)
	}
	digest, err := manifestDigest(descriptor)
	if err != nil {
		return Bundle{}, fmt.Errorf("catalog: bundle %q: %w", descriptor.BundleID, err)
	}
	return Bundle{Descriptor: descriptor, ManifestSHA256: digest}, nil
}

// ValidID reports whether an ID is safe to use as a path component.
func ValidID(id string) bool {
	return idPattern.MatchString(id)
}

// ValidDigest reports whether digest is a lowercase SHA-256 value.
func ValidDigest(digest string) bool {
	return sha256Pattern.MatchString(digest)
}

// ValidatePath accepts canonical relative paths. Components are limited to
// ASCII, so Unicode-normalization collisions cannot occur.
func ValidatePath(path string) error {
	parts := strings.Split(path, "/")
	if len(parts) > maxPathComponents {
		return fmt.Errorf("%w: too many components", ErrUnsafePath)
	}
	for _, part := range parts {
		if part == "." || part == ".." || !componentRunes.MatchString(part) {
			return fmt.Errorf("%w: invalid component", ErrUnsafePath)
		}
	}
	return nil
}

func validate(d Descriptor) error {
	if d.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedSchema, d.SchemaVersion)
	}
	for name, id := range map[string]string{"modelId": d.ModelID, "variantId": d.VariantID, "bundleId": d.BundleID} {
		if !ValidID(id) {
			return fmt.Errorf("invalid %s", name)
		}
	}
	for name, value := range map[string]string{
		"catalogVersion": d.CatalogVersion, "displayName": d.DisplayName, "publisher": d.Publisher,
		"description": d.Description, "releaseLabel": d.ReleaseLabel, "format": d.Format, "precision": d.Precision,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("missing %s", name)
		}
	}
	for name, value := range map[string]string{"sourceUrl": d.SourceURL, "modelCardUrl": d.ModelCardURL} {
		if err := validateHTTPS(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if d.Languages == nil {
		return errors.New("languages must be listed")
	}
	if d.Speakers != nil && *d.Speakers <= 0 {
		return errors.New("invalid speakers")
	}
	if d.SampleRateHz != nil && *d.SampleRateHz <= 0 {
		return errors.New("invalid sampleRateHz")
	}
	if err := validateEngine(d.Engine); err != nil {
		return err
	}
	if err := validateArtifacts(d); err != nil {
		return err
	}
	if err := validateLicensing(d.Licensing); err != nil {
		return err
	}
	return validateSpace(d)
}

func validateEngine(engine Engine) error {
	switch engine.Status {
	case EngineUnknown, EngineUnsupported:
		return nil
	case EngineConfirmed:
		if engine.Evidence == nil || len(engine.Constraints) == 0 {
			return errors.New("confirmed engine needs evidence and constraints")
		}
		return nil
	default:
		return errors.New("invalid engine status")
	}
}

func validateHTTPS(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("must be an HTTPS URL without credentials")
	}
	return nil
}

func validateArtifacts(d Descriptor) error {
	if len(d.Artifacts) == 0 || len(d.Artifacts) > MaxArtifacts {
		return errors.New("artifact count out of bounds")
	}
	ids := map[string]bool{}
	files := map[string]bool{}
	voices := map[string]bool{}
	for _, voice := range d.VoiceIDs {
		if !ValidID(voice) || voices[voice] {
			return fmt.Errorf("invalid or duplicate voice %q", voice)
		}
		voices[voice] = false
	}
	for _, artifact := range d.Artifacts {
		if !ValidID(artifact.ArtifactID) || ids[artifact.ArtifactID] {
			return fmt.Errorf("invalid or duplicate artifact %q", artifact.ArtifactID)
		}
		ids[artifact.ArtifactID] = true
		if err := validateArtifact(artifact); err != nil {
			return fmt.Errorf("artifact %q: %w", artifact.ArtifactID, err)
		}
		folded := strings.ToLower(artifact.Path)
		if files[folded] {
			return fmt.Errorf("artifact %q: %w: colliding path", artifact.ArtifactID, ErrUnsafePath)
		}
		files[folded] = true
		if artifact.VoiceID != nil {
			if _, selected := voices[*artifact.VoiceID]; !selected {
				return fmt.Errorf("artifact %q: voice %q is not selected", artifact.ArtifactID, *artifact.VoiceID)
			}
			voices[*artifact.VoiceID] = true
		}
	}
	for path := range files {
		for parent := path; ; {
			index := strings.LastIndexByte(parent, '/')
			if index < 0 {
				break
			}
			parent = parent[:index]
			if files[parent] {
				return fmt.Errorf("%w: file and directory conflict", ErrUnsafePath)
			}
		}
	}
	for voice, covered := range voices {
		if !covered {
			return fmt.Errorf("voice %q has no artifact", voice)
		}
	}
	return nil
}

func validateArtifact(a Artifact) error {
	if err := ValidatePath(a.Path); err != nil {
		return err
	}
	if !repoPattern.MatchString(a.Repository) {
		return errors.New("invalid repository")
	}
	if !revisionPattern.MatchString(a.Revision) {
		return errors.New("revision must be a full immutable commit")
	}
	if a.SizeBytes == nil || *a.SizeBytes < 0 {
		return errors.New("exact size is required")
	}
	if !sha256Pattern.MatchString(a.SHA256) {
		return errors.New("trusted lowercase SHA-256 is required")
	}
	if strings.TrimSpace(a.HashSource) == "" {
		return errors.New("hash provenance is required")
	}
	if _, err := time.Parse(time.DateOnly, a.HashObservedAt); err != nil {
		return errors.New("hash observation date is required")
	}
	if err := validateHTTPS(a.URL); err != nil {
		return err
	}
	parsed, err := url.Parse(a.URL)
	if err != nil {
		return err
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("source URL must not carry a query or fragment")
	}
	if !strings.Contains(parsed.Path, "/"+a.Revision+"/") || !strings.HasSuffix(parsed.Path, "/"+a.Path) {
		return errors.New("source URL must be pinned to the revision and path")
	}
	return nil
}

func validateLicensing(evidence []LicenseEvidence) error {
	if len(evidence) == 0 {
		return errors.New("license evidence is required")
	}
	for _, item := range evidence {
		switch item.Kind {
		case "model", "conversion", "voice", "dataset", "companion":
		default:
			return fmt.Errorf("invalid license kind %q", item.Kind)
		}
		switch item.DistributionReview {
		case ReviewUnreviewed, ReviewApproved, ReviewBlocked:
		default:
			return fmt.Errorf("invalid distribution review %q", item.DistributionReview)
		}
		if strings.TrimSpace(item.Subject) == "" || strings.TrimSpace(item.SourceRevision) == "" {
			return errors.New("license evidence needs a subject and source revision")
		}
		if err := validateHTTPS(item.SourceURL); err != nil {
			return fmt.Errorf("license source: %w", err)
		}
	}
	return nil
}

func validateSpace(d Descriptor) error {
	var total int64
	for _, artifact := range d.Artifacts {
		total += artifact.Size()
	}
	space := d.Space
	if space.RequiredBytes != total {
		return fmt.Errorf("requiredBytes %d does not match artifact total %d", space.RequiredBytes, total)
	}
	if space.InstalledBytes < total || space.StagingHeadroomBytes < 0 || space.CopyHeadroomBytes < 0 {
		return errors.New("invalid space requirements")
	}
	return nil
}
