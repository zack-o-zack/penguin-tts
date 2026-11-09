package models

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"
)

// sampleRate smooths transfer speed over this run only, so a resumed prefix
// never counts as speed. A restarted file begins a new measurement.
func (j *job) sampleRate(now time.Time) {
	transferred, _ := j.snapshotBytes()
	if j.rateAt.IsZero() || transferred < j.rateMark {
		j.rate, j.rateAt, j.rateMark = 0, now, transferred
		return
	}
	current := float64(transferred-j.rateMark) / now.Sub(j.rateAt).Seconds()
	if j.rate > 0 {
		current = rateSmoothing*current + (1-rateSmoothing)*j.rate
	}
	j.rate, j.rateAt, j.rateMark = current, now, transferred
}

func (j *job) snapshotBytes() (transferred, verified int64) {
	return sumSizes(j.fileBytes), sumSizes(j.verified)
}

func (m *Manager) snapshotLocked(j *job) JobSnapshot {
	transferred, verified := j.snapshotBytes()
	snapshot := JobSnapshot{
		JobID:            j.JobID,
		Kind:             string(j.Kind),
		BundleID:         j.BundleID,
		State:            string(j.State),
		Sequence:         j.sequence,
		TransferredBytes: transferred,
		VerifiedBytes:    verified,
		RetainedBytes:    j.retained,
		CurrentArtifact:  j.current,
		ErrorCode:        string(j.ErrorCode),
		ErrorArtifact:    j.ErrorArtifact,
		Actions:          j.actions(),
		CreatedAt:        j.CreatedAt,
		DisplayName:      cmp.Or(j.BundleID, "Unknown download"),
	}
	if j.ErrorCode != "" {
		snapshot.ErrorMessage = newError(j.ErrorCode, j.ErrorArtifact, nil).Error()
	}
	if bundle, ok := m.catalog.Find(j.BundleID); ok {
		snapshot.DisplayName = bundle.DisplayName
		if bundle.ManifestSHA256 == j.ManifestSHA256 {
			snapshot.TotalBytes = bundle.Space.RequiredBytes
		}
	}
	if j.running {
		snapshot.RetainedBytes = transferred
	}
	if j.running && j.stop == stopNone && j.rate > 0 && (j.State == StateDownloading || j.State == StateCopying) {
		snapshot.BytesPerSecond = int64(j.rate)
		if snapshot.TotalBytes > transferred {
			snapshot.SecondsRemaining = int64(math.Ceil(float64(snapshot.TotalBytes-transferred) / j.rate))
		}
	}
	return snapshot
}

func (j *job) actions() []string {
	actions := []string{}
	if j.running && j.stop != stopNone {
		return actions
	}
	switch j.State {
	case StateQueued, StateDownloading, StateCopying, StateVerifying:
		return []string{"pause", "cancel"}
	case StatePaused, StateInterrupted:
		return []string{"resume", "cancel"}
	case StateFailed:
		if j.unresumable() {
			return []string{"cancel"}
		}
		return []string{"resume", "cancel"}
	}
	return actions
}

// ListJobs returns an authoritative snapshot.
func (m *Manager) ListJobs() JobList {
	var list JobList
	_ = m.withLock(func() error {
		list.Sequence = m.sequence
		list.Jobs = make([]JobSnapshot, 0, len(m.jobs))
		for _, j := range m.jobs {
			list.Jobs = append(list.Jobs, m.snapshotLocked(j))
		}
		return nil
	})
	slices.SortFunc(list.Jobs, func(a, b JobSnapshot) int {
		if order := a.CreatedAt.Compare(b.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.JobID, b.JobID)
	})
	return list
}

// ListCatalog describes bundles without exposing download URLs.
func (m *Manager) ListCatalog() []CatalogEntry {
	entries := make([]CatalogEntry, 0, len(m.catalog.Bundles))
	for _, bundle := range m.catalog.Bundles {
		entries = append(entries, newCatalogEntry(bundle))
	}
	return entries
}
