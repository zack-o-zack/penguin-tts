import { Events } from "@wailsio/runtime";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  type CatalogEntry,
  type InstallationList,
  type JobSnapshot,
  Service,
} from "../../bindings/penguin.local/app/internal/models";

const stateLabels: Record<string, string> = {
  queued: "Waiting",
  downloading: "Downloading",
  copying: "Copying",
  verifying: "Checking files",
  publishing: "Installing",
  installed: "Files verified",
  paused: "Paused",
  interrupted: "Interrupted",
  failed: "Failed",
};

export function jobStateLabel(state: string) {
  return stateLabels[state] ?? state;
}

const settledStates = new Set(["paused", "interrupted", "failed"]);

type JobMap = ReadonlyMap<string, JobSnapshot>;

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

export function useModelLibrary() {
  const [catalog, setCatalog] = useState<CatalogEntry[]>([]);
  const [jobs, setJobs] = useState<JobMap>(new Map());
  const [installations, setInstallations] = useState<InstallationList | null>(
    null,
  );
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");
  const removedJobs = useRef(new Map<string, number>());
  const refreshRef = useRef<() => Promise<void>>(async () => {});

  useEffect(() => {
    let active = true;

    const refreshInstallations = async () => {
      const list = await Service.ListInstallations();
      if (active) {
        setInstallations((current) =>
          current && current.sequence > list.sequence ? current : list,
        );
      }
    };
    refreshRef.current = refreshInstallations;

    const applyJob = (job: JobSnapshot) => {
      if (job.removed) {
        removedJobs.current.set(job.jobId, job.sequence);
      } else if (removedJobs.current.has(job.jobId)) {
        return; // Job IDs are never reused, so this is a delayed event.
      }
      setJobs((current) => {
        const existing = current.get(job.jobId);
        if (existing && existing.sequence >= job.sequence) return current;
        const next = new Map(current);
        if (job.removed) next.delete(job.jobId);
        else next.set(job.jobId, job);
        return next;
      });
      if (job.removed) return;
      setAnnouncement(`${job.displayName}: ${jobStateLabel(job.state)}`);
      if (settledStates.has(job.state)) {
        void refreshInstallations().catch(() => {});
      }
    };

    const offJob = Events.On("models:job", (event) => {
      const job: JobSnapshot = event.data;
      applyJob(job);
    });
    const offInstallations = Events.On("models:installations", () => {
      void refreshInstallations().catch(() => {});
    });

    // Subscribe first, so nothing published during the fetch is missed.
    const loadAll = async () => {
      try {
        const [entries, list] = await Promise.all([
          Service.ListCatalog(),
          Service.ListJobs(),
          refreshInstallations(),
        ]);
        if (!active) return;
        setCatalog(entries ?? []);
        setJobs((current) => {
          const next = new Map<string, JobSnapshot>();
          for (const job of list.jobs ?? []) {
            const removedAt = removedJobs.current.get(job.jobId) ?? -1;
            const newer = current.get(job.jobId);
            if (removedAt >= job.sequence) continue;
            next.set(
              job.jobId,
              newer && newer.sequence > job.sequence ? newer : job,
            );
          }
          for (const [id, job] of current) {
            if (!next.has(id) && job.sequence > list.sequence) {
              next.set(id, job);
            }
          }
          return next;
        });
        setError(null);
      } catch (loadError) {
        if (active) setError(errorMessage(loadError));
      } finally {
        if (active) setLoaded(true);
      }
    };
    void loadAll();

    return () => {
      active = false;
      offJob();
      offInstallations();
    };
  }, []);

  const run = useCallback(async (action: () => Promise<unknown>) => {
    try {
      setError(null);
      await action();
      await refreshRef.current();
    } catch (actionError) {
      setError(errorMessage(actionError));
    }
  }, []);

  return {
    catalog,
    jobs: [...jobs.values()].filter((job) => job.state !== "installed"),
    installations,
    loaded,
    error,
    announcement,
    run,
  };
}
