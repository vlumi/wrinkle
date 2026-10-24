// Logging as key=value lines on stderr, at the level WRINKLE_LOG_LEVEL sets
// (debug, info, warn, error; info by default).
const levels = { debug: 0, info: 1, warn: 2, error: 3 } as const;
type Level = keyof typeof levels;
const threshold: number = (levels as Record<string, number>)[process.env["WRINKLE_LOG_LEVEL"] ?? "info"] ?? levels.info;

const show = (v: unknown): string => {
  if (v instanceof Error) return JSON.stringify(v.message);
  if (Buffer.isBuffer(v)) return v.toString("hex");
  if (typeof v === "string") return /[\s"=]/.test(v) ? JSON.stringify(v) : v;
  return String(v);
};

export class Logger {
  private readonly fields: string;
  constructor(fields = "") {
    this.fields = fields;
  }
  with(attrs: Record<string, unknown>): Logger {
    return new Logger(
      this.fields +
        Object.entries(attrs)
          .map(([k, v]) => ` ${k}=${show(v)}`)
          .join(""),
    );
  }
  private write(level: Level, msg: string, attrs: Record<string, unknown>) {
    if (levels[level] < threshold) return;
    const extra = Object.entries(attrs)
      .map(([k, v]) => ` ${k}=${show(v)}`)
      .join("");
    process.stderr.write(
      `time=${new Date().toISOString()} level=${level.toUpperCase()} msg=${show(msg)}${this.fields}${extra}\n`,
    );
  }
  debug(msg: string, attrs: Record<string, unknown> = {}) {
    this.write("debug", msg, attrs);
  }
  info(msg: string, attrs: Record<string, unknown> = {}) {
    this.write("info", msg, attrs);
  }
  warn(msg: string, attrs: Record<string, unknown> = {}) {
    this.write("warn", msg, attrs);
  }
  error(msg: string, attrs: Record<string, unknown> = {}) {
    this.write("error", msg, attrs);
  }
}

export const log = new Logger();
