package models

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"penguin.local/app/internal/catalog"
)

const (
	appDirectory       = "app.penguin.tts"
	eventJob           = "models:job"
	eventInstallations = "models:installations"
)

func init() {
	application.RegisterEvent[JobSnapshot](eventJob)
	application.RegisterEvent[uint64](eventInstallations)
}

// EnqueueResult identifies the job or existing install satisfying a request.
type EnqueueResult struct {
	JobID          string `json:"jobId"`
	InstallationID string `json:"installationId"`
}

// JobSnapshot is a point-in-time view of a job. Sequence orders snapshots.
type JobSnapshot struct {
	JobID            string    `json:"jobId"`
	Kind             string    `json:"kind"`
	BundleID         string    `json:"bundleId"`
	DisplayName      string    `json:"displayName"`
	State            string    `json:"state"`
	Sequence         uint64    `json:"sequence"`
	Removed          bool      `json:"removed"`
	TotalBytes       int64     `json:"totalBytes"`
	TransferredBytes int64     `json:"transferredBytes"`
	VerifiedBytes    int64     `json:"verifiedBytes"`
	RetainedBytes    int64     `json:"retainedBytes"`
	BytesPerSecond   int64     `json:"bytesPerSecond"`
	SecondsRemaining int64     `json:"secondsRemaining"`
	CurrentArtifact  string    `json:"currentArtifact"`
	ErrorCode        string    `json:"errorCode"`
	ErrorMessage     string    `json:"errorMessage"`
	ErrorArtifact    string    `json:"errorArtifact"`
	Actions          []string  `json:"actions"`
	CreatedAt        time.Time `json:"createdAt"`
}

// JobList is an authoritative job snapshot.
type JobList struct {
	Sequence uint64        `json:"sequence"`
	Jobs     []JobSnapshot `json:"jobs"`
}

// InstallationSnapshot describes installed files. Verified files say nothing
// about whether a speech engine can load them; EngineStatus reports that.
type InstallationSnapshot struct {
	ID             string    `json:"id"`
	BundleID       string    `json:"bundleId"`
	DisplayName    string    `json:"displayName"`
	ManifestSHA256 string    `json:"manifestSha256"`
	Ownership      string    `json:"ownership"`
	Status         string    `json:"status"`
	Location       string    `json:"location"`
	SizeBytes      int64     `json:"sizeBytes"`
	LastVerifiedAt time.Time `json:"lastVerifiedAt"`
	ErrorCode      string    `json:"errorCode"`
	ErrorMessage   string    `json:"errorMessage"`
	ErrorArtifact  string    `json:"errorArtifact"`
	EngineStatus   string    `json:"engineStatus"`
	Current        bool      `json:"current"`
}

// InstallationList reports installs and storage ownership.
type InstallationList struct {
	Sequence      uint64                 `json:"sequence"`
	Installations []InstallationSnapshot `json:"installations"`
	ModelsPath    string                 `json:"modelsPath"`
	ManagedBytes  int64                  `json:"managedBytes"`
	ExternalBytes int64                  `json:"externalBytes"`
	RetainedBytes int64                  `json:"retainedBytes"`
	Notice        string                 `json:"notice"`
}

// ArtifactInfo names one file a folder must contain.
type ArtifactInfo struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
}

// LicenseInfo is one piece of license evidence.
type LicenseInfo struct {
	Kind        string `json:"kind"`
	Subject     string `json:"subject"`
	LicenseID   string `json:"licenseId"`
	Review      string `json:"review"`
	Attribution string `json:"attribution"`
}

// CatalogEntry is the frontend view of a bundle. Download URLs stay in Go.
type CatalogEntry struct {
	BundleID                  string         `json:"bundleId"`
	ModelID                   string         `json:"modelId"`
	VariantID                 string         `json:"variantId"`
	DisplayName               string         `json:"displayName"`
	Publisher                 string         `json:"publisher"`
	Description               string         `json:"description"`
	ReleaseLabel              string         `json:"releaseLabel"`
	ManifestSHA256            string         `json:"manifestSha256"`
	Languages                 []string       `json:"languages"`
	VoiceIDs                  []string       `json:"voiceIds"`
	SampleRateHz              *int           `json:"sampleRateHz"`
	Format                    string         `json:"format"`
	Precision                 string         `json:"precision"`
	RequiredBytes             int64          `json:"requiredBytes"`
	Artifacts                 []ArtifactInfo `json:"artifacts"`
	Licenses                  []LicenseInfo  `json:"licenses"`
	EngineStatus              string         `json:"engineStatus"`
	DownloadEligible          bool           `json:"downloadEligible"`
	DownloadUnavailableReason string         `json:"downloadUnavailableReason"`
	ModelCardURL              string         `json:"modelCardUrl"`
}

func newCatalogEntry(bundle catalog.Bundle) CatalogEntry {
	eligibility := bundle.DownloadEligibility()
	artifacts := make([]ArtifactInfo, 0, len(bundle.Artifacts))
	for _, artifact := range bundle.Artifacts {
		artifacts = append(artifacts, ArtifactInfo{Path: artifact.Path, SizeBytes: artifact.Size()})
	}
	licenses := make([]LicenseInfo, 0, len(bundle.Licensing))
	for _, evidence := range bundle.Licensing {
		license := LicenseInfo{Kind: evidence.Kind, Subject: evidence.Subject, Review: evidence.DistributionReview, Attribution: evidence.Attribution}
		if evidence.LicenseID != nil {
			license.LicenseID = *evidence.LicenseID
		}
		licenses = append(licenses, license)
	}
	return CatalogEntry{
		BundleID:                  bundle.BundleID,
		ModelID:                   bundle.ModelID,
		VariantID:                 bundle.VariantID,
		DisplayName:               bundle.DisplayName,
		Publisher:                 bundle.Publisher,
		Description:               bundle.Description,
		ReleaseLabel:              bundle.ReleaseLabel,
		ManifestSHA256:            bundle.ManifestSHA256,
		Languages:                 bundle.Languages,
		VoiceIDs:                  bundle.VoiceIDs,
		SampleRateHz:              bundle.SampleRateHz,
		Format:                    bundle.Format,
		Precision:                 bundle.Precision,
		RequiredBytes:             bundle.Space.RequiredBytes,
		Artifacts:                 artifacts,
		Licenses:                  licenses,
		EngineStatus:              bundle.Engine.Status,
		DownloadEligible:          eligibility.Eligible,
		DownloadUnavailableReason: eligibility.Reason,
		ModelCardURL:              bundle.ModelCardURL,
	}
}

// Config supplies native integration; zero fields use production defaults.
type Config struct {
	Root            string
	Catalog         *catalog.Catalog
	Transport       http.RoundTripper
	EmitJob         func(JobSnapshot)
	EmitInstalled   func(uint64)
	ChooseFolder    func(title string, bundleID string) (string, error)
	RevealDirectory func(path string) error
}

// Service is the narrow surface bound to the frontend.
type Service struct {
	config  Config
	manager *Manager
	failure error
}

// NewService creates a service that opens storage during application startup.
func NewService(config Config) *Service {
	return &Service{config: config}
}

// WailsService binds the service using the application's event and dialog APIs.
func WailsService(app *application.App, config Config) application.Service {
	config.EmitJob = func(snapshot JobSnapshot) { app.Event.Emit(eventJob, snapshot) }
	config.EmitInstalled = func(sequence uint64) { app.Event.Emit(eventInstallations, sequence) }
	if config.ChooseFolder == nil {
		config.ChooseFolder = func(title, _ string) (string, error) {
			return app.Dialog.OpenFile().
				SetTitle(title).
				CanChooseDirectories(true).
				CanChooseFiles(false).
				PromptForSingleSelection()
		}
	}
	if config.RevealDirectory == nil {
		config.RevealDirectory = func(path string) error { return app.Env.OpenFileManager(path, false) }
	}
	return application.NewService(NewService(config))
}

// ServiceStartup keeps the app usable when storage cannot be opened, so the
// models page can explain why (for example, another running instance).
func (s *Service) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	s.manager, s.failure = s.open(ctx)
	if s.failure != nil {
		log.Printf("model storage unavailable: %s", asError(s.failure).Code)
	}
	return nil
}

func (s *Service) open(ctx context.Context) (*Manager, error) {
	root := s.config.Root
	if root == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return nil, newError(CodeStorageUnavailable, "", err)
		}
		root = filepath.Join(configDir, appDirectory)
	}
	loaded := s.config.Catalog
	if loaded == nil {
		var err error
		if loaded, err = catalog.Load(); err != nil {
			return nil, newError(CodeInvalidManifest, "", err)
		}
	}
	return Open(ctx, Options{
		Root:            root,
		Catalog:         loaded,
		Transport:       s.config.Transport,
		OnJob:           s.config.EmitJob,
		OnInstallations: s.config.EmitInstalled,
	})
}

// ServiceShutdown stops workers; recovery handles anything left unfinished.
func (s *Service) ServiceShutdown() error {
	if s.manager == nil {
		return nil
	}
	return s.manager.Close()
}

func (s *Service) ready() (*Manager, error) {
	if s.manager != nil {
		return s.manager, nil
	}
	if s.failure != nil {
		return nil, s.failure
	}
	return nil, newError(CodeStorageUnavailable, "", errors.New("storage has not started"))
}

// ListCatalog returns the bundles in this build's reviewed catalog.
func (s *Service) ListCatalog() ([]CatalogEntry, error) {
	manager, err := s.ready()
	if err != nil {
		return nil, err
	}
	return manager.ListCatalog(), nil
}

// ListInstallations returns installs and storage usage.
func (s *Service) ListInstallations() (InstallationList, error) {
	manager, err := s.ready()
	if err != nil {
		return InstallationList{}, err
	}
	return manager.ListInstallations(), nil
}

// ListJobs returns an authoritative job snapshot.
func (s *Service) ListJobs() (JobList, error) {
	manager, err := s.ready()
	if err != nil {
		return JobList{}, err
	}
	return manager.ListJobs(), nil
}

// EnqueueDownload queues a catalog bundle and returns immediately.
func (s *Service) EnqueueDownload(bundleID string) (EnqueueResult, error) {
	manager, err := s.ready()
	if err != nil {
		return EnqueueResult{}, err
	}
	return manager.EnqueueDownload(bundleID)
}

// Pause stops a job and keeps its partial files.
func (s *Service) Pause(jobID string) error {
	return s.withManager(func(manager *Manager) error { return manager.Pause(jobID) })
}

// Resume continues paused, interrupted, or failed work.
func (s *Service) Resume(jobID string) error {
	return s.withManager(func(manager *Manager) error { return manager.Resume(jobID) })
}

// Cancel stops a job and deletes its partial files.
func (s *Service) Cancel(jobID string) error {
	return s.withManager(func(manager *Manager) error { return manager.Cancel(jobID) })
}

func (s *Service) withManager(action func(*Manager) error) error {
	manager, err := s.ready()
	if err != nil {
		return err
	}
	return action(manager)
}

// ImportFolder asks for a folder and queues its verification. mode is
// "reference" or "copy". An empty result means the user dismissed the picker.
func (s *Service) ImportFolder(bundleID string, mode string) (EnqueueResult, error) {
	manager, err := s.ready()
	if err != nil {
		return EnqueueResult{}, err
	}
	folder, err := s.choose("Choose the folder containing this model", bundleID)
	if err != nil || folder == "" {
		return EnqueueResult{}, err
	}
	return manager.ImportFolder(bundleID, folder, ImportMode(mode))
}

// LocateAgain asks for a folder and verifies it as an external install's new location.
func (s *Service) LocateAgain(installationID string) (EnqueueResult, error) {
	manager, err := s.ready()
	if err != nil {
		return EnqueueResult{}, err
	}
	var bundleID string
	for _, install := range manager.ListInstallations().Installations {
		if install.ID == installationID {
			bundleID = install.BundleID
		}
	}
	folder, err := s.choose("Locate this model's folder", bundleID)
	if err != nil || folder == "" {
		return EnqueueResult{}, err
	}
	return manager.LocateAgain(installationID, folder)
}

func (s *Service) choose(title, bundleID string) (string, error) {
	if s.config.ChooseFolder == nil {
		return "", newError(CodeStorageUnavailable, "", errors.New("folder selection is unavailable"))
	}
	folder, err := s.config.ChooseFolder(title, bundleID)
	if err != nil {
		return "", newError(CodeLocationUnavailable, "", err)
	}
	return folder, nil
}

// Revalidate fully re-hashes an installation.
func (s *Service) Revalidate(ctx context.Context, installationID string) error {
	return s.withManager(func(manager *Manager) error { return manager.Revalidate(ctx, installationID) })
}

// RemoveInstallation forgets an external folder or deletes managed files.
func (s *Service) RemoveInstallation(installationID string) error {
	return s.withManager(func(manager *Manager) error { return manager.RemoveInstallation(installationID) })
}

// RevealModelsFolder opens the managed models folder in the file manager.
func (s *Service) RevealModelsFolder() error {
	manager, err := s.ready()
	if err != nil {
		return err
	}
	if s.config.RevealDirectory == nil {
		return newError(CodeStorageUnavailable, "", errors.New("file manager is unavailable"))
	}
	if err := s.config.RevealDirectory(manager.ListInstallations().ModelsPath); err != nil {
		return newError(CodeLocationUnavailable, "", err)
	}
	return nil
}
