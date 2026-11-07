import { ArrowUpIcon, AudioLinesIcon } from "lucide-react";
import { type CSSProperties, useRef } from "react";
import mark from "@/assets/brand/penguin-mark.svg";
import inverseMark from "@/assets/brand/penguin-mark-inverse.svg";
import { AppearanceControls } from "@/components/appearance-controls";
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

function Workspace() {
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
        role="complementary"
        aria-label="Sidebar"
        aria-hidden={!expanded}
        inert={!expanded}
        className="border-foreground/10 bg-background/15 pt-(--header-height) [&_[data-slot=sidebar-inner]]:bg-transparent"
      >
        <SidebarContent id="app-sidebar" className="p-4 pt-5">
          <nav aria-label="Workspace">
            <SidebarMenu>
              <SidebarMenuItem>
                <SidebarMenuButton asChild isActive size="lg">
                  <a href="#speech" aria-current="page">
                    <AudioLinesIcon aria-hidden="true" />
                    <span>Text to speech</span>
                  </a>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
          </nav>
        </SidebarContent>
        <SidebarFooter className="px-6 pb-5 text-xs leading-relaxed text-muted-foreground">
          <p>Local first. Open source.</p>
        </SidebarFooter>
      </Sidebar>
      <main
        id="speech"
        className="flex min-w-0 flex-1 items-center justify-center px-4 pt-[calc(var(--header-height)+3rem)] pb-12 sm:px-6 lg:px-16"
      >
        <SpeechInput />
      </main>
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
