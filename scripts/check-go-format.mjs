import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const result = spawnSync("gofmt", ["-l", "."], {
  cwd: fileURLToPath(new URL("../app/", import.meta.url)),
  encoding: "utf8",
});

if (result.error) {
  throw result.error;
}

if (result.status !== 0) {
  process.stderr.write(result.stderr);
  process.exit(result.status ?? 1);
}

if (result.stdout.trim()) {
  process.stderr.write(
    `Run pnpm format to format these Go files:\n${result.stdout}`,
  );
  process.exit(1);
}
