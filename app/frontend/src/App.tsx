import {
  ArrowUpIcon,
  AudioLinesIcon,
  PanelLeftCloseIcon,
  PanelLeftOpenIcon,
} from "lucide-react";
import { useState } from "react";
import mark from "@/assets/brand/penguin-mark.svg";
import inverseMark from "@/assets/brand/penguin-mark-inverse.svg";
import { AppearanceControls } from "@/components/appearance-controls";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { version } from "../package.json";

function SpeechInput() {
  return (
    <section className="speech-start" aria-labelledby="welcome-title">
      <p className="eyebrow">YOUR WORDS, YOUR VOICE</p>
      <h1 id="welcome-title">What would you like to say?</h1>
      <div className="speech-composer">
        <Label className="sr-only" htmlFor="speech-text">
          Text to speak
        </Label>
        <Textarea
          className="min-h-24 resize-y rounded-none border-0 bg-transparent p-1 text-base leading-relaxed shadow-none [field-sizing:fixed] focus-visible:ring-0 md:text-base dark:bg-transparent"
          id="speech-text"
          placeholder="Write or paste your text here…"
          rows={3}
          aria-describedby="speech-status"
        />
        <div className="composer-footer">
          <span className="composer-label">
            <AudioLinesIcon size={16} aria-hidden="true" />
            Text to speech
          </span>
          <Button
            variant="ghost"
            size="icon"
            className="generate-button rounded-full bg-foreground/6 text-muted-foreground disabled:opacity-100"
            type="button"
            disabled
            aria-label="Generate speech (coming soon)"
          >
            <ArrowUpIcon size={19} aria-hidden="true" />
          </Button>
        </div>
      </div>
      <p id="speech-status" className="speech-status">
        Just a space for your words. Speech generation is coming next.
      </p>
    </section>
  );
}

export default function App() {
  const [sidebarExpanded, setSidebarExpanded] = useState(true);
  const ToggleIcon = sidebarExpanded ? PanelLeftCloseIcon : PanelLeftOpenIcon;

  return (
    <div className="workspace" data-sidebar-expanded={sidebarExpanded}>
      <header className="workspace-header">
        <Button
          variant="ghost"
          size="icon"
          className="sidebar-toggle text-muted-foreground"
          type="button"
          aria-label={sidebarExpanded ? "Collapse sidebar" : "Expand sidebar"}
          aria-expanded={sidebarExpanded}
          aria-controls="app-sidebar"
          title={sidebarExpanded ? "Collapse sidebar" : "Expand sidebar"}
          onClick={() => setSidebarExpanded((expanded) => !expanded)}
        >
          <ToggleIcon size={16} aria-hidden="true" />
        </Button>
        <div className="brand">
          <img
            className="brand-light"
            src={mark}
            alt="Penguin"
            draggable={false}
            width={18}
            height={18}
          />
          <img
            className="brand-dark"
            src={inverseMark}
            alt="Penguin"
            draggable={false}
            width={18}
            height={18}
          />
          <span className="brand-version">v{version}</span>
        </div>
        <AppearanceControls />
      </header>
      <aside
        id="app-sidebar"
        className="app-sidebar"
        aria-label="Sidebar"
        hidden={!sidebarExpanded}
      >
        <div id="sidebar-content" className="sidebar-content">
          <nav aria-label="Workspace">
            <Button
              asChild
              variant="ghost"
              className="sidebar-link h-auto w-full justify-start"
            >
              <a
                href="#speech"
                aria-current="page"
                aria-label="Text to speech"
                title={sidebarExpanded ? undefined : "Text to speech"}
              >
                <AudioLinesIcon size={19} aria-hidden="true" />
                <span hidden={!sidebarExpanded}>Text to speech</span>
              </a>
            </Button>
          </nav>
          <p className="sidebar-note" hidden={!sidebarExpanded}>
            Local first. Open source.
          </p>
        </div>
      </aside>
      <main id="speech" className="speech-workspace">
        <SpeechInput />
      </main>
    </div>
  );
}
