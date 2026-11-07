import { SlidersHorizontalIcon } from "lucide-react";
import { useLayoutEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Slider } from "@/components/ui/slider";

type Appearance = {
  theme: "light" | "dark";
  transparency: number;
};

const storageKey = "penguin.appearance";
const defaultAppearance: Appearance = { theme: "dark", transparency: 12 };

function readAppearance(): Appearance {
  try {
    const saved: unknown = JSON.parse(
      localStorage.getItem(storageKey) ?? "null",
    );
    if (
      saved &&
      typeof saved === "object" &&
      "theme" in saved &&
      "transparency" in saved &&
      (saved.theme === "dark" || saved.theme === "light") &&
      typeof saved.transparency === "number" &&
      Number.isFinite(saved.transparency)
    ) {
      return {
        theme: saved.theme,
        transparency: Math.min(20, Math.max(0, saved.transparency)),
      };
    }
  } catch {
    // Appearance still works when saved preferences are unavailable.
  }
  return defaultAppearance;
}

export function AppearanceControls() {
  const [appearance, setAppearance] = useState(readAppearance);

  useLayoutEffect(() => {
    document.documentElement.classList.toggle(
      "dark",
      appearance.theme === "dark",
    );
    document.documentElement.style.setProperty(
      "--surface-opacity",
      `${100 - appearance.transparency}%`,
    );
    try {
      localStorage.setItem(storageKey, JSON.stringify(appearance));
    } catch {
      // Keep the current session usable if preference storage is disabled.
    }
  }, [appearance]);

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          type="button"
          className="ml-auto size-9 text-muted-foreground"
          aria-label="Appearance"
          title="Appearance"
        >
          <SlidersHorizontalIcon size={18} aria-hidden="true" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="end"
        sideOffset={10}
        className="space-y-3 p-4"
        aria-labelledby="appearance-title"
      >
        <h2 id="appearance-title" className="font-semibold">
          Appearance
        </h2>
        <Label htmlFor="theme">Theme</Label>
        <Select
          value={appearance.theme}
          onValueChange={(theme) =>
            setAppearance({
              ...appearance,
              theme: theme === "light" ? "light" : "dark",
            })
          }
        >
          <SelectTrigger id="theme" className="mt-2 w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="dark">Dark</SelectItem>
            <SelectItem value="light">Light</SelectItem>
          </SelectContent>
        </Select>
        <div className="flex items-center justify-between gap-2">
          <Label id="transparency-label">Transparency</Label>
          <output htmlFor="transparency" className="text-sm tabular-nums">
            {appearance.transparency}%
          </output>
        </div>
        <Slider
          id="transparency"
          className="my-3"
          aria-labelledby="transparency-label"
          min={0}
          max={20}
          step={1}
          value={[appearance.transparency]}
          onValueChange={([transparency]) => {
            if (transparency !== undefined) {
              setAppearance({ ...appearance, transparency });
            }
          }}
        />
        <div
          className="flex items-center justify-between gap-2 text-xs text-muted-foreground"
          aria-hidden="true"
        >
          <span>Solid</span>
          <span>Frosted</span>
        </div>
      </PopoverContent>
    </Popover>
  );
}
