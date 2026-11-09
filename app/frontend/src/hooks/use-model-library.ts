import { Events } from "@wailsio/runtime";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  type CatalogEntry,
  type InstallationList,
  type JobList,
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

function mergeJob(current: JobMap, job: JobSnapshot): JobMap {
  const existing = current.get(job.jobId);
  if (existing && existing.sequence >= job.sequence) return current;
  const next = new Map(current);
  if (job.removed) next.delete(job.jobId);
  else next.set(job.jobId, job);
  return next;
}

// Events delivered during the fetch stay when they are newer than the list.
function mergeJobList(
  current: JobMap,
  list: JobList,
  removed: ReadonlySet<string>,
): JobMap {
  const next = new Map<string, JobSnapshot>();
  for (const job of list.jobs ?? []) {
    if (removed.has(job.jobId)) continue;
    const newer = current.get(job.jobId);
    next.set(job.jobId, newer && newer.sequence > job.sequence ? newer : job);
  }
  for (const [id, job] of current) {
    if (!next.has(id) && job.sequence > list.sequence) next.set(id, job);
  }
  return next;
}

function useInstallations() {
  const [installations, setInstallations] = useState<InstallationList | null>(
    null,
  );

  const refresh = useCallback(async () => {
    const list = await Service.ListInstallations();
    setInstallations((current) =>
      current && current.sequence > list.sequence ? current : list,
    );
  }, []);

  useEffect(
    () =>
      Events.On("models:installations", () => {
        void refresh().catch(() => {});
      }),
    [refresh],
  );

  return { installations, refresh };
}

export function useModelLibrary() {
  const { installations, refresh } = useInstallations();
  const [catalog, setCatalog] = useState<CatalogEntry[]>([]);
  const [jobs, setJobs] = useState<JobMap>(new Map());
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");
  const removedJobs = useRef(new Set<string>());

  useEffect(() => {
    let active = true;

    const applyJob = (job: JobSnapshot) => {
      // Job IDs are never reused, so anything after removal is a delayed event.
      if (removedJobs.current.has(job.jobId)) return;
      if (job.removed) removedJobs.current.add(job.jobId);
      setJobs((current) => mergeJob(current, job));
      if (job.removed) return;
      setAnnouncement(`${job.displayName}: ${jobStateLabel(job.state)}`);
      if (settledStates.has(job.state)) void refresh().catch(() => {});
    };

    const offJob = Events.On("models:job", (event) => {
      applyJob(event.data);
    });

    // Subscribe first, so nothing published during the fetch is missed.
    const loadAll = async () => {
      try {
        const [entries, list] = await Promise.all([
          Service.ListCatalog(),
          Service.ListJobs(),
          refresh(),
        ]);
        if (!active) return;
        setCatalog(entries ?? []);
        setJobs((current) => mergeJobList(current, list, removedJobs.current));
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
    };
  }, [refresh]);

  const run = useCallback(
    async (action: () => Promise<unknown>) => {
      try {
        setError(null);
        await action();
        await refresh();
      } catch (actionError) {
        setError(errorMessage(actionError));
      }
    },
    [refresh],
  );

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
