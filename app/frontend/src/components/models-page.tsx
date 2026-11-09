import {
  AlertCircleIcon,
  DownloadIcon,
  EllipsisIcon,
  FolderOpenIcon,
} from "lucide-react";
import { Children, type ReactNode, useId, useState } from "react";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent } from "@/components/ui/collapsible";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemSeparator,
  ItemTitle,
} from "@/components/ui/item";
import { Progress } from "@/components/ui/progress";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Spinner } from "@/components/ui/spinner";
import { jobStateLabel, useModelLibrary } from "@/hooks/use-model-library";
import {
  type CatalogEntry,
  type InstallationSnapshot,
  type JobSnapshot,
  Service,
} from "../../bindings/penguin.local/app/internal/models";

type Run = (action: () => Promise<unknown>) => Promise<void>;

const units = ["B", "KB", "MB", "GB", "TB"];

function formatBytes(bytes: number) {
  let value = bytes;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit += 1;
  }
  return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: unit === 0 ? 0 : 1 }).format(value)} ${units[unit]}`;
}

const engineLabels: Record<string, string> = {
  unknown: "Speech engine compatibility unknown",
  confirmed: "Works with Penguin's speech engine",
  unsupported: "Not supported by Penguin's speech engine",
};

function formatTimeLeft(seconds: number) {
  if (seconds < 60) return `${seconds} sec left`;
  const minutes = Math.ceil(seconds / 60);
  if (minutes < 60) return `${minutes} min left`;
  return `${Math.floor(minutes / 60)} hr ${minutes % 60} min left`;
}

const languageNames = new Intl.DisplayNames(undefined, { type: "language" });

function formatLanguages(tags: string[] | null) {
  return (tags ?? [])
    .map((tag) => {
      try {
        return languageNames.of(tag) ?? tag;
      } catch {
        return tag;
      }
    })
    .join(", ");
}

function engineLabel(status: string) {
  return engineLabels[status] ?? engineLabels.unknown;
}

function jobProgress(job: JobSnapshot) {
  if (job.totalBytes <= 0 || job.state === "publishing") return null;
  const done =
    job.state === "verifying"
      ? job.verifiedBytes
      : Math.max(job.transferredBytes, job.retainedBytes);
  return Math.min(100, Math.round((done / job.totalBytes) * 100));
}

function jobDetail(job: JobSnapshot, percent: number | null) {
  if (job.state === "publishing") return "Finishing installation";
  if (percent === null) return "Size unknown";
  if (job.state === "verifying") {
    return `${formatBytes(job.verifiedBytes)} of ${formatBytes(job.totalBytes)} checked`;
  }
  const done = Math.max(job.transferredBytes, job.retainedBytes);
  return [
    `${formatBytes(done)} of ${formatBytes(job.totalBytes)}`,
    job.bytesPerSecond > 0 && `${formatBytes(job.bytesPerSecond)}/s`,
    job.secondsRemaining > 0 && formatTimeLeft(job.secondsRemaining),
  ]
    .filter(Boolean)
    .join(" · ");
}

function MoreMenu({ name, children }: { name: string; children: ReactNode }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          size="icon-sm"
          variant="ghost"
          aria-label={`More options for ${name}`}
        >
          <EllipsisIcon aria-hidden="true" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-auto">
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  keepLabel,
  actionLabel,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  keepLabel: string;
  actionLabel: string;
  onConfirm: () => void;
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent size="sm">
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{keepLabel}</AlertDialogCancel>
          <AlertDialogAction onClick={onConfirm}>
            {actionLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function cancelCopy(job: JobSnapshot) {
  if (job.state === "failed") {
    return {
      title: `Dismiss ${job.displayName}?`,
      description: "Penguin deletes the partial files for this model.",
      keepLabel: "Keep",
      actionLabel: "Dismiss",
    };
  }
  if (job.kind === "reference") {
    return {
      title: `Cancel checking ${job.displayName}?`,
      description:
        "Penguin stops checking this folder. Your files stay where they are.",
      keepLabel: "Keep checking",
      actionLabel: "Cancel",
    };
  }
  const noun = job.kind === "copy" ? "copy" : "download";
  return {
    title: `Cancel this ${noun}?`,
    description: `${job.displayName} stops and its progress is deleted. To keep progress, use Pause instead.`,
    keepLabel: `Keep ${job.kind === "copy" ? "copying" : "downloading"}`,
    actionLabel: `Cancel ${noun}`,
  };
}

function RowError({ message }: { message: string }) {
  return (
    <Alert variant="destructive" className="basis-full">
      <AlertCircleIcon aria-hidden="true" />
      <AlertDescription>{message}</AlertDescription>
    </Alert>
  );
}

function JobRow({ job, run }: { job: JobSnapshot; run: Run }) {
  const nameId = useId();
  const percent = jobProgress(job);
  const actions = job.actions ?? [];
  const canCancel = actions.includes("cancel");
  const [confirmingCancel, setConfirmingCancel] = useState(false);
  const failedWithoutFiles = job.state === "failed" && job.retainedBytes === 0;
  const cancel = () => void run(() => Service.Cancel(job.jobId));
  return (
    <Item role="group" aria-labelledby={nameId}>
      <ItemContent>
        <ItemTitle id={nameId} className="text-[13px]">
          {job.displayName}
        </ItemTitle>
        <ItemDescription className="text-[11px] tabular-nums">
          <span>{jobStateLabel(job.state)}</span> · {jobDetail(job, percent)}
        </ItemDescription>
      </ItemContent>
      <ItemActions>
        {actions.includes("pause") && (
          <Button
            size="sm"
            variant="secondary"
            onClick={() => void run(() => Service.Pause(job.jobId))}
          >
            Pause
          </Button>
        )}
        {actions.includes("resume") && (
          <Button
            size="sm"
            variant="secondary"
            onClick={() => void run(() => Service.Resume(job.jobId))}
          >
            {job.state === "failed" ? "Retry" : "Resume"}
          </Button>
        )}
        {canCancel && (
          <MoreMenu name={job.displayName}>
            <DropdownMenuItem
              onSelect={() =>
                failedWithoutFiles ? cancel() : setConfirmingCancel(true)
              }
            >
              {job.state === "failed" ? "Dismiss" : "Cancel"}
            </DropdownMenuItem>
          </MoreMenu>
        )}
      </ItemActions>
      <Progress
        className="basis-full"
        aria-label={`${job.displayName} progress`}
        value={percent}
      />
      {job.errorMessage && <RowError message={job.errorMessage} />}
      <ConfirmDialog
        open={confirmingCancel}
        onOpenChange={setConfirmingCancel}
        onConfirm={cancel}
        {...cancelCopy(job)}
      />
    </Item>
  );
}

function DetailList({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1">
      <h3 className="text-[11px] font-semibold text-muted-foreground">
        {title}
      </h3>
      <ItemGroup className="gap-0 has-data-[size=xs]:gap-0">
        {children}
      </ItemGroup>
    </div>
  );
}

function BundleRow({
  entry,
  managed,
  run,
}: {
  entry: CatalogEntry;
  managed: InstallationSnapshot | undefined;
  run: Run;
}) {
  const nameId = useId();
  const [showDetails, setShowDetails] = useState(false);
  return (
    <Item role="group" aria-labelledby={nameId}>
      <ItemContent>
        <ItemTitle id={nameId} className="text-[13px]">
          {entry.displayName}
        </ItemTitle>
        <ItemDescription className="text-[11px]">
          {[
            entry.downloadEligible
              ? formatBytes(entry.requiredBytes)
              : `Download unavailable. ${entry.downloadUnavailableReason}`,
            formatLanguages(entry.languages),
          ]
            .filter(Boolean)
            .join(" · ")}
        </ItemDescription>
      </ItemContent>
      <ItemActions>
        <Button
          size="sm"
          variant="secondary"
          disabled={!entry.downloadEligible}
          onClick={() =>
            void run(() => Service.EnqueueDownload(entry.bundleId))
          }
        >
          {managed ? "Repair" : "Download"}
        </Button>
        <MoreMenu name={entry.displayName}>
          <DropdownMenuItem
            onSelect={() =>
              void run(() => Service.ImportFolder(entry.bundleId, "reference"))
            }
          >
            Use existing folder
          </DropdownMenuItem>
          <DropdownMenuItem
            onSelect={() =>
              void run(() => Service.ImportFolder(entry.bundleId, "copy"))
            }
          >
            Copy into Penguin
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={() => setShowDetails(!showDetails)}>
            {showDetails ? "Hide details" : "Show details"}
          </DropdownMenuItem>
        </MoreMenu>
      </ItemActions>
      <Collapsible
        open={showDetails}
        className="basis-full data-[state=closed]:hidden"
      >
        <CollapsibleContent className="flex flex-col gap-3 pt-2">
          <ItemDescription className="text-[11px]">
            {entry.publisher} · {entry.releaseLabel}. {entry.description}
          </ItemDescription>
          <div className="flex flex-wrap gap-1.5">
            <Badge variant="outline">{entry.format}</Badge>
            <Badge variant="outline">{entry.precision}</Badge>
            {(entry.languages ?? []).map((language) => (
              <Badge key={language} variant="outline">
                {language}
              </Badge>
            ))}
            <Badge variant="secondary">{engineLabel(entry.engineStatus)}</Badge>
          </div>
          <DetailList title="Required files">
            {(entry.artifacts ?? []).map((artifact) => (
              <Item key={artifact.path} size="xs" className="px-0 py-1">
                <ItemContent>
                  <ItemTitle className="font-mono text-xs break-all">
                    {artifact.path}
                  </ItemTitle>
                </ItemContent>
                <ItemActions className="text-xs text-muted-foreground tabular-nums">
                  {formatBytes(artifact.sizeBytes)}
                </ItemActions>
              </Item>
            ))}
          </DetailList>
          <DetailList title="Licenses and attribution">
            {(entry.licenses ?? []).map((license) => (
              <Item key={license.subject} size="xs" className="px-0 py-1">
                <ItemContent>
                  <ItemTitle className="text-xs">
                    {license.licenseId || "License unresolved"}
                  </ItemTitle>
                  <ItemDescription className="text-xs">
                    {license.attribution}
                  </ItemDescription>
                </ItemContent>
              </Item>
            ))}
          </DetailList>
        </CollapsibleContent>
      </Collapsible>
    </Item>
  );
}

const statusLabels: Record<string, string> = {
  verified_files: "Files verified",
  unavailable: "Unavailable",
  changed: "Files changed",
};

function InstallationRow({
  install,
  run,
}: {
  install: InstallationSnapshot;
  run: Run;
}) {
  const nameId = useId();
  const [showDetails, setShowDetails] = useState(false);
  const [confirmingRemoval, setConfirmingRemoval] = useState(false);
  const external = install.ownership === "external";
  const verified = install.status === "verified_files";
  const removeLabel = external ? "Remove from Penguin" : "Delete files";
  return (
    <Item role="group" aria-labelledby={nameId}>
      <ItemContent>
        <ItemTitle id={nameId} className="text-[13px]">
          {install.displayName}
        </ItemTitle>
        <ItemDescription className="text-[11px] tabular-nums">
          {formatBytes(install.sizeBytes)}
        </ItemDescription>
      </ItemContent>
      <ItemActions>
        <Badge
          variant={verified ? "ghost" : "destructive"}
          className={verified ? "px-0 text-muted-foreground" : undefined}
        >
          {statusLabels[install.status] ?? install.status}
        </Badge>
        <MoreMenu name={install.displayName}>
          <DropdownMenuItem
            onSelect={() => void run(() => Service.Revalidate(install.id))}
          >
            Check files
          </DropdownMenuItem>
          {external && (
            <DropdownMenuItem
              onSelect={() => void run(() => Service.LocateAgain(install.id))}
            >
              Locate again
            </DropdownMenuItem>
          )}
          <DropdownMenuItem onSelect={() => setShowDetails(!showDetails)}>
            {showDetails ? "Hide details" : "Show details"}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="destructive"
            onSelect={() => setConfirmingRemoval(true)}
          >
            {removeLabel}
          </DropdownMenuItem>
        </MoreMenu>
      </ItemActions>
      {install.errorMessage && <RowError message={install.errorMessage} />}
      <Collapsible
        open={showDetails}
        className="basis-full data-[state=closed]:hidden"
      >
        <CollapsibleContent className="flex flex-col gap-2 pt-2">
          <div className="flex flex-wrap gap-1.5">
            <Badge variant="outline">
              {external ? "External folder" : "Managed by Penguin"}
            </Badge>
            <Badge variant="secondary">
              {engineLabel(install.engineStatus)}
            </Badge>
          </div>
          <ItemDescription className="text-xs break-all">
            {install.location}
          </ItemDescription>
        </CollapsibleContent>
      </Collapsible>
      <ConfirmDialog
        open={confirmingRemoval}
        onOpenChange={setConfirmingRemoval}
        title={
          external
            ? `Remove ${install.displayName} from Penguin?`
            : `Delete ${install.displayName}?`
        }
        description={
          external
            ? "Penguin forgets this folder. Your folder and its files stay where they are."
            : "Penguin deletes its copy of these files. This cannot be undone."
        }
        keepLabel="Keep"
        actionLabel={removeLabel}
        onConfirm={() => void run(() => Service.RemoveInstallation(install.id))}
      />
    </Item>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id} className="flex flex-col gap-1.5">
      <h2 id={id} className="px-3 text-[13px] font-semibold">
        {title}
      </h2>
      <ItemGroup className="gap-0 rounded-[10px] bg-foreground/[0.04] ring-1 ring-foreground/10 has-data-[size=sm]:gap-0 has-data-[size=xs]:gap-0">
        {Children.map(children, (row, index) => (
          <>
            {index > 0 && (
              <ItemSeparator className="mx-3 my-0 bg-foreground/10 data-horizontal:w-auto" />
            )}
            {row}
          </>
        ))}
      </ItemGroup>
    </section>
  );
}

const libraryDescription =
  "Download a speech model or add one you already have.";

function ModelLibrary() {
  const library = useModelLibrary();
  const { catalog, jobs, installations, loaded, error, announcement, run } =
    library;
  const installs = installations?.installations ?? [];
  const available = catalog.filter(
    (entry) =>
      !jobs.some((job) => job.bundleId === entry.bundleId) &&
      !installs.some(
        (install) =>
          install.bundleId === entry.bundleId &&
          install.current &&
          install.status === "verified_files",
      ),
  );
  return (
    <div className="flex flex-col gap-5">
      <p role="status" aria-live="polite" className="sr-only">
        {announcement}
      </p>
      {error && (
        <Alert variant="destructive" role="alert">
          <AlertCircleIcon aria-hidden="true" />
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {installations?.notice && (
        <Alert>
          <AlertCircleIcon aria-hidden="true" />
          <AlertDescription>{installations.notice}</AlertDescription>
        </Alert>
      )}
      {!loaded && <Spinner className="size-5 text-muted-foreground" />}
      {jobs.length > 0 && (
        <Section title="In progress">
          {jobs.map((job) => (
            <JobRow key={job.jobId} job={job} run={run} />
          ))}
        </Section>
      )}
      {installs.length > 0 && (
        <Section title="Installed">
          {installs.map((install) => (
            <InstallationRow key={install.id} install={install} run={run} />
          ))}
        </Section>
      )}
      {available.length > 0 && (
        <Section title="Available">
          {available.map((entry) => (
            <BundleRow
              key={entry.bundleId}
              entry={entry}
              managed={installs.find(
                (install) =>
                  install.bundleId === entry.bundleId &&
                  install.ownership === "managed" &&
                  install.current,
              )}
              run={run}
            />
          ))}
        </Section>
      )}
      {installations && (installs.length > 0 || jobs.length > 0) && (
        <Item size="xs" role="group" aria-label="Storage" className="px-3 py-0">
          <ItemContent>
            <ItemDescription className="text-[11px] tabular-nums">
              {formatBytes(
                installations.managedBytes + installations.retainedBytes,
              )}{" "}
              used
            </ItemDescription>
          </ItemContent>
          <ItemActions>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label="Open models folder"
              title="Open models folder"
              onClick={() => void run(() => Service.RevealModelsFolder())}
            >
              <FolderOpenIcon aria-hidden="true" />
            </Button>
          </ItemActions>
        </Item>
      )}
    </div>
  );
}

export function ModelsPage() {
  return (
    <div className="mx-auto flex w-full max-w-[720px] flex-col gap-6">
      <header className="flex flex-col gap-1">
        <h1 className="text-[22px] font-semibold tracking-tight">Models</h1>
        <p className="text-[13px] text-muted-foreground">
          {libraryDescription}
        </p>
      </header>
      <ModelLibrary />
    </div>
  );
}

export function ModelsDialog() {
  const label = "Models and downloads";
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button
          variant="ghost"
          size="icon-lg"
          className="text-muted-foreground"
          aria-label={label}
          title={label}
        >
          <DownloadIcon aria-hidden="true" className="size-5" />
        </Button>
      </DialogTrigger>
      <DialogContent className="flex max-h-[81svh] flex-col gap-0 overflow-hidden rounded-xl p-0 shadow-2xl sm:max-w-[min(936px,calc(100%-4rem))] [&>[data-slot=dialog-close]]:right-auto [&>[data-slot=dialog-close]]:left-2 max-sm:top-auto max-sm:bottom-0 max-sm:left-0 max-sm:max-h-[calc(100svh-3rem)] max-sm:max-w-none max-sm:translate-x-0 max-sm:translate-y-0 max-sm:rounded-none">
        <DialogHeader className="h-11 shrink-0 justify-center border-b border-foreground/10 px-12 text-center">
          <DialogTitle className="text-[13px] font-semibold">
            Models
          </DialogTitle>
          <DialogDescription className="sr-only">
            {libraryDescription}
          </DialogDescription>
        </DialogHeader>
        <ScrollArea className="flex min-h-0 flex-col overflow-hidden [&_[data-slot=scroll-area-viewport]]:min-h-0 [&_[data-slot=scroll-area-viewport]]:flex-1 [&_[data-slot=scroll-area-viewport]>div]:block!">
          <div className="p-5">
            <ModelLibrary />
          </div>
        </ScrollArea>
      </DialogContent>
    </Dialog>
  );
}
