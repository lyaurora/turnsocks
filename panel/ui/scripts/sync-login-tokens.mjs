// Syncs the CSS design tokens from styles.css into the login page embedded in
// panel/server/ui.go, between the tokens:sync-start / tokens:sync-end markers.
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const uiDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const cssPath = resolve(uiDir, "src/styles.css");
const goPath = resolve(uiDir, "../server/ui.go");

const css = readFileSync(cssPath, "utf8");

function extractVars(selector) {
  const block = css.match(new RegExp(`(?:^|\\n)${selector.replace(".", "\\.")} \\{([\\s\\S]*?)\\n\\}`));
  if (!block) throw new Error(`selector ${selector} not found in styles.css`);
  const lines = [];
  for (const line of block[1].split("\n")) {
    const trimmed = line.trim();
    if (trimmed.startsWith("color-scheme:") || trimmed.startsWith("--")) {
      // layout-only vars used by the SPA theme animation are meaningless on the login page
      if (trimmed.startsWith("--theme-")) continue;
      lines.push(`      ${trimmed}`);
    }
  }
  return lines.join("\n");
}

const generated = [
  "    /* tokens:sync-start -- generated from panel/ui/src/styles.css, do not edit by hand */",
  "    :root {",
  extractVars(":root"),
  "    }",
  "    .dark {",
  extractVars(".dark"),
  "    }",
  "    /* tokens:sync-end */"
].join("\n");

const go = readFileSync(goPath, "utf8");
const pattern = /[ \t]*\/\* tokens:sync-start[\s\S]*?tokens:sync-end \*\//;
if (!pattern.test(go)) throw new Error("token sync markers not found in ui.go");
const next = go.replace(pattern, generated);
if (next !== go) {
  writeFileSync(goPath, next);
  console.log("login tokens synced into panel/server/ui.go");
} else {
  console.log("login tokens already in sync");
}
