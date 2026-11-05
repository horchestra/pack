// The little YAML pack.yaml needs: maps (keys sorted), lists, strings.

const PLAIN = /^[A-Za-z0-9_./@+][A-Za-z0-9_./:@+ ,-]*$/; // a comma is plain in block context
const RESERVED = /^(true|false|null|yes|no|on|off|y|n|~)$/i;
const NUMBER = /^[-+]?(\d[\d_]*)?\.?\d*([eE][-+]?\d+)?$|^0[xob][0-9a-fA-F_]+$/;

function scalar(s: string): string {
  const plain = PLAIN.test(s) && !s.endsWith(":") && !s.endsWith(" ") && !s.includes(": ")
    && !RESERVED.test(s) && !NUMBER.test(s);
  return plain ? s : JSON.stringify(s);
}

export function dumpYaml(v: unknown): string {
  return [...lines(v, 0)].join("");
}

function* lines(v: unknown, indent: number): Generator<string> {
  const pad = "  ".repeat(indent);
  if (typeof v === "object" && v !== null && !Array.isArray(v)) {
    for (const k of Object.keys(v as Record<string, unknown>).sort()) {
      const e = (v as Record<string, unknown>)[k];
      if (typeof e === "object" && e !== null) {
        yield `${pad}${scalar(k)}:\n`;
        yield* lines(e, indent + (Array.isArray(e) ? 0 : 1));
      } else {
        yield `${pad}${scalar(k)}: ${scalar(String(e))}\n`;
      }
    }
  } else if (Array.isArray(v)) {
    for (const e of v) {
      if (typeof e === "object" && e !== null && !Array.isArray(e)) {
        let first = true;
        for (const line of lines(e, indent + 1)) {
          yield first ? `${pad}- ${line.slice(pad.length + 2)}` : line;
          first = false;
        }
      } else {
        yield `${pad}- ${scalar(String(e))}\n`;
      }
    }
  }
}
