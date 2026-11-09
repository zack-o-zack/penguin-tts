import { tmpdir } from "node:os";
import { join } from "node:path";

export const e2eRoot = join(tmpdir(), "penguin-e2e-9473");
