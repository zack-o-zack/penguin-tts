import { createRoot } from "react-dom/client";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import { Button } from "@/components/ui/button";
import { Calendar } from "@/components/ui/calendar";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Slider } from "@/components/ui/slider";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import "../src/styles.css";

function ComponentFixture() {
  return (
    <TooltipProvider>
      <main className="mx-auto flex max-w-xl flex-col gap-6 p-8">
        <h1 className="text-2xl font-semibold">
          Penguin component accessibility
        </h1>
        <div className="flex flex-wrap gap-3">
          <Button>Primary action</Button>
          <Button variant="secondary">Secondary action</Button>
          <Button variant="outline">Outline action</Button>
          <Button variant="ghost">Ghost action</Button>
          <Button variant="destructive">Delete item</Button>
        </div>
        <Field>
          <FieldLabel htmlFor="voice-name">Voice name</FieldLabel>
          <Input
            id="voice-name"
            name="voice-name"
            autoComplete="off"
            placeholder="For example, Penguin…"
            aria-describedby="voice-description"
          />
          <FieldDescription id="voice-description">
            Give this voice a recognizable name.
          </FieldDescription>
        </Field>
        <Field data-invalid>
          <FieldLabel htmlFor="invalid-name">Invalid name</FieldLabel>
          <Input
            id="invalid-name"
            name="invalid-name"
            autoComplete="off"
            aria-invalid
            aria-describedby="name-error"
          />
          <FieldError id="name-error">Enter a voice name.</FieldError>
        </Field>
        <div className="flex items-center gap-3">
          <Checkbox id="offline" />
          <Label htmlFor="offline">Work offline</Label>
        </div>
        <div className="flex items-center gap-3">
          <Switch id="notifications" />
          <Label htmlFor="notifications">Notifications</Label>
        </div>
        <Field>
          <FieldLabel htmlFor="provider">Provider</FieldLabel>
          <Select defaultValue="local">
            <SelectTrigger id="provider">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="local">Local</SelectItem>
              <SelectItem value="cloud">Cloud</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        <Field>
          <FieldLabel htmlFor="format">Format</FieldLabel>
          <NativeSelect id="format" name="format">
            <NativeSelectOption>WAV</NativeSelectOption>
            <NativeSelectOption>MP3</NativeSelectOption>
          </NativeSelect>
        </Field>
        <Field>
          <FieldLabel id="speed-label">Speed</FieldLabel>
          <Slider aria-labelledby="speed-label" defaultValue={[50]} />
        </Field>
        <Tabs defaultValue="local">
          <TabsList aria-label="Voice source">
            <TabsTrigger value="local">Local voices</TabsTrigger>
            <TabsTrigger value="cloud">Cloud voices</TabsTrigger>
          </TabsList>
          <TabsContent value="local">Local voice details</TabsContent>
          <TabsContent value="cloud">Cloud voice details</TabsContent>
        </Tabs>
        <Accordion type="single" collapsible>
          <AccordionItem value="privacy">
            <AccordionTrigger>Where is my data stored?</AccordionTrigger>
            <AccordionContent>On this device.</AccordionContent>
          </AccordionItem>
        </Accordion>
        <Dialog>
          <DialogTrigger asChild>
            <Button>Open settings</Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Settings</DialogTitle>
              <DialogDescription>Choose how Penguin behaves.</DialogDescription>
            </DialogHeader>
            <Field>
              <FieldLabel htmlFor="dialog-name">Display name</FieldLabel>
              <Input id="dialog-name" name="display-name" autoComplete="off" />
            </Field>
          </DialogContent>
        </Dialog>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button variant="outline">About offline mode</Button>
          </TooltipTrigger>
          <TooltipContent>
            Local models work without a connection.
          </TooltipContent>
        </Tooltip>
        <Calendar mode="single" aria-label="Export date" />
        <Spinner />
      </main>
    </TooltipProvider>
  );
}

const root = document.getElementById("root");
if (!root) throw new Error("The component fixture root is missing.");
createRoot(root).render(<ComponentFixture />);
