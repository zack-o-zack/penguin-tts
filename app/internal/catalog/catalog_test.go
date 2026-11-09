package catalog

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const testRevision = "0123456789abcdef0123456789abcdef01234567"

func artifact(id, path string, size int64) Artifact {
	return Artifact{
		ArtifactID:     id,
		Repository:     "owner/repo",
		Revision:       testRevision,
		Path:           path,
		URL:            "https://example.test/owner/repo/resolve/" + testRevision + "/" + path,
		SizeBytes:      &size,
		SHA256:         strings.Repeat("a", 64),
		HashSource:     "fixture",
		HashObservedAt: "2026-11-09",
	}
}

func descriptor() Descriptor {
	voice := "voice-a"
	license := "Apache-2.0"
	voiceArtifact := artifact("voice", "voices/a.bin", 5)
	voiceArtifact.VoiceID = &voice
	return Descriptor{
		SchemaVersion:  SchemaVersion,
		CatalogVersion: "1",
		ModelID:        "model",
		VariantID:      "variant",
		BundleID:       "bundle",
		VoiceIDs:       []string{voice},
		DisplayName:    "Name",
		Publisher:      "Publisher",
		Description:    "Description",
		SourceURL:      "https://example.test/source",
		ModelCardURL:   "https://example.test/card",
		ReleaseLabel:   "1.0",
		Languages:      []string{"en-US"},
		Format:         "onnx",
		Precision:      "int8",
		Engine:         Engine{Status: EngineUnknown},
		Artifacts:      []Artifact{artifact("model", "model.onnx", 10), voiceArtifact},
		Licensing: []LicenseEvidence{{
			Kind: "model", Subject: "Model", SourceURL: "https://example.test/license",
			SourceRevision: testRevision, LicenseID: &license, DistributionReview: ReviewApproved,
		}},
		Space: Space{RequiredBytes: 15, InstalledBytes: 15},
	}
}

func TestValidationRejectsUnsafeDescriptors(t *testing.T) {
	size := int64(-1)
	cases := map[string]func(*Descriptor){
		"bad bundle id":         func(d *Descriptor) { d.BundleID = "../bundle" },
		"uppercase bundle id":   func(d *Descriptor) { d.BundleID = "Bundle" },
		"unsupported schema":    func(d *Descriptor) { d.SchemaVersion = 2 },
		"no artifacts":          func(d *Descriptor) { d.Artifacts = nil },
		"traversal":             func(d *Descriptor) { d.Artifacts[0].Path = "../outside" },
		"absolute":              func(d *Descriptor) { d.Artifacts[0].Path = "/absolute" },
		"drive":                 func(d *Descriptor) { d.Artifacts[0].Path = "C:/drive" },
		"backslash":             func(d *Descriptor) { d.Artifacts[0].Path = `a\b` },
		"empty component":       func(d *Descriptor) { d.Artifacts[0].Path = "a//b" },
		"dot component":         func(d *Descriptor) { d.Artifacts[0].Path = "a/./b" },
		"non-ascii":             func(d *Descriptor) { d.Artifacts[0].Path = "caf\u00e9.onnx" },
		"too deep":              func(d *Descriptor) { d.Artifacts[0].Path = "a/b/c/d/e/f/g/h/i" },
		"duplicate artifact id": func(d *Descriptor) { d.Artifacts[1].ArtifactID = d.Artifacts[0].ArtifactID },
		"duplicate path":        func(d *Descriptor) { d.Artifacts[1].Path = d.Artifacts[0].Path },
		"case collision":        func(d *Descriptor) { d.Artifacts[1].Path = "MODEL.onnx" },
		"file directory clash":  func(d *Descriptor) { d.Artifacts[1].Path = "model.onnx/voice.bin" },
		"missing hash":          func(d *Descriptor) { d.Artifacts[0].SHA256 = "" },
		"uppercase hash":        func(d *Descriptor) { d.Artifacts[0].SHA256 = strings.Repeat("A", 64) },
		"missing provenance":    func(d *Descriptor) { d.Artifacts[0].HashSource = "" },
		"missing observed date": func(d *Descriptor) { d.Artifacts[0].HashObservedAt = "" },
		"missing size":          func(d *Descriptor) { d.Artifacts[0].SizeBytes = nil },
		"negative size":         func(d *Descriptor) { d.Artifacts[0].SizeBytes = &size },
		"non-https":             func(d *Descriptor) { d.Artifacts[0].URL = strings.Replace(d.Artifacts[0].URL, "https", "http", 1) },
		"credentials in url": func(d *Descriptor) {
			d.Artifacts[0].URL = strings.Replace(d.Artifacts[0].URL, "https://", "https://a:b@", 1)
		},
		"query in url":    func(d *Descriptor) { d.Artifacts[0].URL += "?token=1" },
		"moving revision": func(d *Descriptor) { d.Artifacts[0].Revision = "main" },
		"unpinned url":    func(d *Descriptor) { d.Artifacts[0].URL = "https://example.test/resolve/main/model.onnx" },
		"url path mismatch": func(d *Descriptor) {
			d.Artifacts[0].URL = strings.Replace(d.Artifacts[0].URL, "model.onnx", "other.onnx", 1)
		},
		"no license evidence":   func(d *Descriptor) { d.Licensing = nil },
		"wrong total":           func(d *Descriptor) { d.Space.RequiredBytes = 14 },
		"unknown voice":         func(d *Descriptor) { d.VoiceIDs = []string{"voice-b"} },
		"unselected voice file": func(d *Descriptor) { d.VoiceIDs = nil },
		"confirmed no evidence": func(d *Descriptor) { d.Engine.Status = EngineConfirmed },
		"unknown languages":     func(d *Descriptor) { d.Languages = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := descriptor()
			d.Artifacts = append([]Artifact{}, d.Artifacts...)
			mutate(&d)
			if _, err := NewBundle(d); err == nil {
				t.Fatal("descriptor was accepted")
			}
		})
	}
}

func TestArtifactCountBound(t *testing.T) {
	d := descriptor()
	d.VoiceIDs, d.Artifacts = nil, nil
	for i := range MaxArtifacts + 1 {
		d.Artifacts = append(d.Artifacts, artifact("a"+string(rune('a'+i%26))+strings.Repeat("x", i%90)+itoa(i), "f"+itoa(i), 0))
	}
	d.Space = Space{}
	if _, err := NewBundle(d); err == nil {
		t.Fatal("oversized manifest was accepted")
	}
	d.Artifacts = d.Artifacts[:MaxArtifacts]
	if _, err := NewBundle(d); err != nil {
		t.Fatalf("bounded manifest rejected: %v", err)
	}
}

func itoa(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestParseRejectsMalformedDocuments(t *testing.T) {
	cases := map[string]string{
		"not json":         `{`,
		"unknown major":    `{"schemaVersion":2,"descriptors":[]}`,
		"unknown field":    `{"schemaVersion":1,"descriptors":[],"extra":true}`,
		"trailing content": `{"schemaVersion":1,"descriptors":[]} {}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(input)); err == nil {
				t.Fatal("document was accepted")
			}
		})
	}
	if _, err := Parse([]byte(`{"schemaVersion":2}`)); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("schema error not reported: %v", err)
	}
	d := descriptor()
	document, err := json.Marshal(file{SchemaVersion: 1, Descriptors: []Descriptor{d, d}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(document); err == nil {
		t.Fatal("duplicate bundle was accepted")
	}
}

func TestDigestIgnoresOrderAndCoversReviewedMetadata(t *testing.T) {
	base, err := NewBundle(descriptor())
	if err != nil {
		t.Fatal(err)
	}
	reordered := descriptor()
	reordered.Artifacts[0], reordered.Artifacts[1] = reordered.Artifacts[1], reordered.Artifacts[0]
	again, err := NewBundle(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if base.ManifestSHA256 != again.ManifestSHA256 {
		t.Fatal("artifact order changed the digest")
	}

	changes := map[string]func(*Descriptor){
		"hash":     func(d *Descriptor) { d.Artifacts[0].SHA256 = strings.Repeat("b", 64) },
		"source":   func(d *Descriptor) { d.Artifacts[0].HashSource = "other" },
		"license":  func(d *Descriptor) { d.Licensing[0].DistributionReview = ReviewBlocked },
		"revision": func(d *Descriptor) { d.Licensing[0].SourceRevision = "other" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			d := descriptor()
			change(&d)
			changed, err := NewBundle(d)
			if err != nil {
				t.Fatal(err)
			}
			if changed.ManifestSHA256 == base.ManifestSHA256 {
				t.Fatal("digest ignored a reviewed field")
			}
		})
	}
}

func TestDigestSurvivesSerializationReload(t *testing.T) {
	d := descriptor()
	first, err := NewBundle(d)
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(file{SchemaVersion: SchemaVersion, Descriptors: []Descriptor{first.Descriptor}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Parse(document)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Bundles[0].ManifestSHA256 != first.ManifestSHA256 {
		t.Fatal("digest changed after reload")
	}
}

func TestVoiceSelectionsAreSeparateBundles(t *testing.T) {
	first, err := NewBundle(descriptor())
	if err != nil {
		t.Fatal(err)
	}
	voice := "voice-b"
	other := descriptor()
	other.BundleID = "bundle-b"
	other.VoiceIDs = []string{voice}
	otherVoice := artifact("voice", "voices/b.bin", 7)
	otherVoice.VoiceID = &voice
	other.Artifacts[1] = otherVoice
	other.Space = Space{RequiredBytes: 17, InstalledBytes: 17}
	second, err := NewBundle(other)
	if err != nil {
		t.Fatal(err)
	}
	if first.ManifestSHA256 == second.ManifestSHA256 || first.Space.RequiredBytes == second.Space.RequiredBytes {
		t.Fatal("voice selection did not change bundle identity or totals")
	}
}

func TestCanonicalizeMatchesRFC8785(t *testing.T) {
	input := map[string]any{"\u20ac": 1, "\r": "a\"<>&\u2028\n\x01", "1": []any{nil, true}, "\U0001F600": 2, "\ufb33": 3}
	got, err := Canonicalize(input)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"\\r\":\"a\\\"<>&\u2028\\n\\u0001\",\"1\":[null,true],\"\u20ac\":1,\"\U0001F600\":2,\"\ufb33\":3}"
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if _, err := Canonicalize(map[string]any{"a": 1.5}); err == nil {
		t.Fatal("non-integer number was accepted")
	}
}

func TestEligibility(t *testing.T) {
	cases := map[string]struct {
		mutate   func(*Descriptor)
		eligible bool
	}{
		"approved":   {func(*Descriptor) {}, true},
		"unreviewed": {func(d *Descriptor) { d.Licensing[0].DistributionReview = ReviewUnreviewed }, false},
		"blocked":    {func(d *Descriptor) { d.Licensing[0].DistributionReview = ReviewBlocked }, false},
		"unresolved": {func(d *Descriptor) { d.Licensing[0].LicenseID = nil }, false},
		"gated":      {func(d *Descriptor) { d.Licensing[0].AcceptanceRequired = true }, false},
		"no model":   {func(d *Descriptor) { d.Licensing[0].Kind = "voice" }, false},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			d := descriptor()
			test.mutate(&d)
			bundle, err := NewBundle(d)
			if err != nil {
				t.Fatal(err)
			}
			got := bundle.DownloadEligibility()
			if got.Eligible != test.eligible || (!got.Eligible && got.Reason == "") {
				t.Fatalf("eligibility = %+v", got)
			}
		})
	}
}

func TestEmbeddedCatalogIsValidAndDownloadable(t *testing.T) {
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	bundle, ok := loaded.Find("kokoro-v1-onnx-int8-af-heart")
	if !ok {
		t.Fatal("first candidate is missing")
	}
	if bundle.Space.RequiredBytes != 92887010 {
		t.Fatalf("total = %d", bundle.Space.RequiredBytes)
	}
	if bundle.Engine.Status != EngineUnknown {
		t.Fatalf("engine status = %s", bundle.Engine.Status)
	}
	if eligibility := bundle.DownloadEligibility(); !eligibility.Eligible {
		t.Fatalf("eligibility = %+v", eligibility)
	}
	kinds := map[string]bool{}
	for _, evidence := range bundle.Licensing {
		kinds[evidence.Kind] = true
		if evidence.LicenseID == nil || evidence.Attribution == "" {
			t.Fatalf("evidence is incomplete: %+v", evidence)
		}
	}
	for _, kind := range []string{"model", "conversion", "voice", "dataset", "companion"} {
		if !kinds[kind] {
			t.Fatalf("missing %s evidence", kind)
		}
	}
	if !ValidDigest(bundle.ManifestSHA256) {
		t.Fatalf("digest = %q", bundle.ManifestSHA256)
	}
}
