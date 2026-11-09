import {
  ArrowUpIcon,
  AudioLinesIcon,
  BoxIcon,
  type LucideIcon,
} from "lucide-react";
import { type CSSProperties, useRef, useSyncExternalStore } from "react";
import mark from "@/assets/brand/penguin-mark.svg";
import inverseMark from "@/assets/brand/penguin-mark-inverse.svg";
import { AppearanceControls } from "@/components/appearance-controls";
import { ModelsDialog, ModelsPage } from "@/components/models-page";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupText,
  InputGroupTextarea,
} from "@/components/ui/input-group";
import { Label } from "@/components/ui/label";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarTrigger,
  useSidebar,
} from "@/components/ui/sidebar";
import { version } from "../package.json";

function SpeechInput() {
  return (
    <section
      className="w-full max-w-[680px] pb-10 text-center"
      aria-labelledby="welcome-title"
    >
      <p className="text-[11px] font-medium tracking-[0.16em] text-muted-foreground">
        YOUR WORDS, YOUR VOICE
      </p>
      <h1
        id="welcome-title"
        className="mt-3 mb-8 text-[clamp(28px,3.3vw,40px)] leading-tight font-medium tracking-[-0.04em] text-balance"
      >
        What would you like to say?
      </h1>
      <Label className="sr-only" htmlFor="speech-text">
        Text to speak
      </Label>
      <InputGroup className="rounded-2xl border-foreground/15 bg-card/40 text-left has-disabled:bg-card/40 has-disabled:opacity-100 has-[[data-slot=input-group-control]:focus-visible]:ring-1 dark:bg-card/40 dark:has-disabled:bg-card/40">
        <InputGroupTextarea
          id="speech-text"
          className="min-h-32 max-h-[40svh] resize-y px-5 pt-5 text-base leading-relaxed md:text-base"
          placeholder="Write or paste your text here…"
          rows={3}
          aria-describedby="speech-status"
        />
        <InputGroupAddon
          align="block-end"
          className="justify-between px-5 pb-4"
        >
          <InputGroupText className="text-xs">
            <AudioLinesIcon aria-hidden="true" />
            Text to speech
          </InputGroupText>
          <InputGroupButton
            size="icon-sm"
            variant="secondary"
            className="rounded-full"
            disabled
            aria-label="Generate speech (coming soon)"
          >
            <ArrowUpIcon aria-hidden="true" />
          </InputGroupButton>
        </InputGroupAddon>
      </InputGroup>
      <p
        id="speech-status"
        className="mt-4 text-xs leading-relaxed text-balance text-muted-foreground"
      >
        Just a space for your words. Speech generation is coming next.
      </p>
    </section>
  );
}

function subscribeToHash(onChange: () => void) {
  window.addEventListener("hashchange", onChange);
  return () => window.removeEventListener("hashchange", onChange);
}

function ViewLink({
  href,
  active,
  icon: Icon,
  label,
}: {
  href: string;
  active: boolean;
  icon: LucideIcon;
  label: string;
}) {
  return (
    <SidebarMenuItem>
      <SidebarMenuButton asChild isActive={active} size="lg">
        <a href={href} aria-current={active ? "page" : undefined}>
          <Icon aria-hidden="true" />
          <span>{label}</span>
        </a>
      </SidebarMenuButton>
    </SidebarMenuItem>
  );
}

function currentView() {
  return window.location.hash === "#models" ? "models" : "speech";
}

function Workspace() {
  const view = useSyncExternalStore(subscribeToHash, currentView);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const { open, openMobile, isMobile } = useSidebar();
  const expanded = isMobile ? openMobile : open;
  const sidebarLabel = expanded ? "Collapse sidebar" : "Expand sidebar";

  return (
    <>
      <header className="fixed inset-x-0 top-0 z-20 flex h-(--header-height) min-w-0 items-center gap-3 pr-4 pl-[100px] select-none [--wails-draggable:drag] [&_button]:[--wails-draggable:no-drag]">
        <SidebarTrigger
          ref={triggerRef}
          className="size-9 text-muted-foreground"
          aria-label={sidebarLabel}
          aria-expanded={expanded}
          aria-controls="app-sidebar"
          title={sidebarLabel}
        />
        <div className="flex shrink-0 items-center gap-2">
          <img
            className="size-[18px] object-contain dark:hidden"
            src={mark}
            alt="Penguin"
            draggable={false}
            width={18}
            height={18}
          />
          <img
            className="hidden size-[18px] object-contain dark:block"
            src={inverseMark}
            alt="Penguin"
            draggable={false}
            width={18}
            height={18}
          />
          <span className="text-xs leading-none text-muted-foreground tabular-nums">
            v{version}
          </span>
        </div>
        <AppearanceControls />
      </header>
      <Sidebar
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          triggerRef.current?.focus();
        }}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          if (event.currentTarget instanceof HTMLElement) {
            event.currentTarget.focus();
          }
        }}
        role="complementary"
        aria-label="Sidebar"
        aria-hidden={!expanded}
        inert={!expanded}
        className="border-foreground/10 bg-background/15 pt-(--header-height) [&_[data-slot=sidebar-inner]]:bg-transparent"
      >
        <SidebarContent id="app-sidebar" className="p-4 pt-5">
          <nav aria-label="Workspace">
            <SidebarMenu>
              <ViewLink
                href="#speech"
                active={view === "speech"}
                icon={AudioLinesIcon}
                label="Text to speech"
              />
              <ViewLink
                href="#models"
                active={view === "models"}
                icon={BoxIcon}
                label="Models"
              />
            </SidebarMenu>
          </nav>
        </SidebarContent>
        <SidebarFooter className="items-start px-4 pb-4">
          <ModelsDialog />
        </SidebarFooter>
      </Sidebar>
      <main
        id="speech"
        hidden={view !== "speech"}
        className="flex min-w-0 flex-1 items-center justify-center px-4 pt-[calc(var(--header-height)+3rem)] pb-12 sm:px-6 lg:px-16"
      >
        <SpeechInput />
      </main>
      {view === "models" && (
        <main
          id="models"
          className="mt-(--header-height) h-[calc(100svh-var(--header-height))] min-w-0 flex-1 overflow-y-auto px-4 pt-6 pb-12 sm:px-6 lg:px-16"
        >
          <ModelsPage />
        </main>
      )}
    </>
  );
}

export default function App() {
  return (
    <SidebarProvider
      data-desktop-shell=""
      className="relative bg-background/[var(--surface-opacity)]"
      style={
        {
          "--sidebar-width": "232px",
          "--header-height": "55px",
        } as CSSProperties
      }
    >
      <Workspace />
    </SidebarProvider>
  );
}
