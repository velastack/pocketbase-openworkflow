// Generates cron_golden.json: occurrences computed by OpenWorkflow's own
// nextScheduleOccurrence (cron-parser + luxon), which engine/cron.go must
// reproduce exactly.
//
//   npm install && node engine/testdata/gen-cron-golden.mjs [count] [out]
//
// The default count/output regenerate the committed corpus. A larger count
// (e.g. 200000 to a scratch path, with OW_CRON_GOLDEN=<path> go test
// ./engine -run TestCronGolden) is a deeper one-off differential check.
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
// The package's exports map hides package.json and dist/core, so go by path.
const owDir = join(here, "../../node_modules/openworkflow");
const { version } = JSON.parse(readFileSync(join(owDir, "package.json"), "utf8"));
// nextScheduleOccurrence is not a public export; load the module directly.
const { nextScheduleOccurrence } = await import(
  pathToFileURL(join(owDir, "dist/core/schedule.js")).href
);

const count = Number(process.argv[2] ?? 6000);
const out = process.argv[3] ?? join(here, "cron_golden.json");

// mulberry32: deterministic so the committed corpus is reproducible.
let seed = 0x5eed;
const rand = () => {
  seed = (seed + 0x6d2b79f5) | 0;
  let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
  t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
};
const int = (lo, hi) => lo + Math.floor(rand() * (hi - lo + 1));
const pick = (xs) => xs[Math.floor(rand() * xs.length)];

// Zones whose future rules change between tzdata releases (Africa/Casablanca,
// Asia/Gaza) are left out: Node's ICU and Go's zoneinfo ship different tzdata
// versions, so they would test the data rather than the port.
const timezones = [
  "UTC", "America/New_York", "Europe/London", "Europe/Berlin",
  "Australia/Lord_Howe", "Asia/Kolkata", "America/Santiago", "America/Havana",
  "America/St_Johns", "Pacific/Chatham", "Australia/Sydney",
  "America/Los_Angeles", "Pacific/Apia", "Asia/Tehran",
];

const fixedCrons = [
  "0 * * * *", "* * * * *", "* * * * * *", "*/15 * * * *", "0 9 * * *",
  "30 2 * * *", "0 2 * * *", "30 1 * * *", "0 1 * * *", "0 0 * * *",
  "0 */2 * * *", "*/30 1-3 * * *", "0 0,30 2-3 * * *", "15 0 * * *",
  "0 23 * * *", "0 0 L * *", "0 12 L 2 *", "0 0 29 2 *", "0 0 31 * *",
  "0 0 * * 5L", "0 0 * * 5#3", "0 9 * * 1#1", "0 0 1,15 * 1", "0 0 13 * 5",
  "0 0 ? * MON-FRI", "0 0 * JAN-MAR SUN", "0 0 * * 0-7", "0 0 * * 7",
  "5-10/2 * * * *", "0 0 1-31 * *", "30 */5 * * * *", "0 0 12 * * 0,6",
  "*/7 */3 * * * *", "0 45 23 31 12 *", "0 0 0 * * 1-5", "59 59 23 * * *",
  "0 0 30 2 *", "0 0 L 2 *", "0 0 * * 1L", "0 0 1 * 3#5", "0 0 8 L * *",
  "0 0 L * 5", "0 30 0 * * *", "0 0 24 * * *", "0 60 * * *", "H * * * *",
  "@daily", "* * *", "* * * * * * *", "0 0 L-2 * *", "0 0 15W * *",
  "0 0 * * L", "0 0 * * 5#6", "0 0 * * 1-3#2", "0 0 32 * *", "0 0 0 * 13 *",
  "0 0 1,1 * *", "*/0 * * * *", "1-60 * * * *", "0 0 * * FOO", "a b c d e",
  "0 0 31 4 *", "0 0 31 4 1", "0 0 */2 * * *", "0 0 2/3 * * *",
];

function randomField(min, max, extras = []) {
  const kind = int(0, 9);
  switch (kind) {
    case 0: case 1: return "*";
    case 2: return String(int(min, max));
    case 3: {
      const n = int(2, 3);
      const vals = new Set();
      while (vals.size < n) vals.add(int(min, max));
      return [...vals].join(",");
    }
    case 4: { const a = int(min, max); return `${a}-${int(a, max)}`; }
    case 5: return `*/${int(1, Math.max(1, Math.floor((max - min) / 2)))}`;
    case 6: { const a = int(min, max); return `${a}-${int(a, max)}/${int(1, 5)}`; }
    case 7: return `${int(min, max)}/${int(1, 7)}`;
    default: return extras.length > 0 ? pick(extras) : String(int(min, max));
  }
}

function randomCron() {
  const fields = [
    randomField(0, 23, ["0-3", "1,2", "2", "1", "0,1,2,3"]),
    randomField(1, 31, ["L", "?", "1", "31", "29", "30", "15,L"]),
    randomField(1, 12, ["JAN", "FEB-APR", "2", "*/3"]),
    randomField(0, 7, ["?", "MON-FRI", "5L", "1#2", "SUN", "0#5", "6#1", "3L"]),
  ];
  const minute = randomField(0, 59, ["0", "30", "0,30"]);
  const seconds = rand() < 0.4 ? [randomField(0, 59, ["0", "30"])] : [];
  return [...seconds, minute, ...fields].join(" ");
}

// Instants around each zone's offset changes (DST) in 2025-2031, plus noise.
function transitions(tz) {
  const fmt = new Intl.DateTimeFormat("en-US", { timeZone: tz, timeZoneName: "longOffset" });
  const off = (ms) => fmt.formatToParts(ms).find((p) => p.type === "timeZoneName").value;
  const result = [];
  const start = Date.UTC(2025, 0, 1);
  const end = Date.UTC(2031, 0, 1);
  let prev = off(start);
  for (let t = start; t < end; t += 3_600_000) {
    const o = off(t);
    if (o !== prev) {
      let lo = t - 3_600_000, hi = t;
      while (hi - lo > 1000) { const mid = Math.floor((lo + hi) / 2); if (off(mid) === prev) lo = mid; else hi = mid; }
      result.push(hi);
      prev = o;
    }
  }
  return result;
}
const zoneTransitions = Object.fromEntries(timezones.map((tz) => [tz, transitions(tz)]));

function randomAfter(tz) {
  const marks = zoneTransitions[tz];
  if (marks.length > 0 && rand() < 0.6) {
    const mark = pick(marks);
    const offsets = [0, 1, -1, 1000, -1000, 60_000, -60_000, 1_800_000, -1_800_000, 3_600_000, -3_600_000, 5_400_000, -5_400_000, -86_400_000, -7_200_000];
    return mark + (rand() < 0.7 ? pick(offsets) : int(-2 * 86_400_000, 86_400_000));
  }
  const t = Date.UTC(2024, 0, 1) + Math.floor(rand() * 8 * 365 * 86_400_000);
  return rand() < 0.5 ? t - (t % 1000) : t;
}

function compute(cron, tz, after) {
  try {
    return { next: nextScheduleOccurrence({ cron, timezone: tz }, new Date(after)).toISOString() };
  } catch {
    return { next: null };
  }
}

const cases = [];
const add = (cron, tz, after) => {
  const r = compute(cron, tz, after);
  cases.push({ cron, tz, after: new Date(after).toISOString(), next: r.next });
  return r.next;
};

for (const cron of fixedCrons) {
  for (const tz of timezones) {
    let after = randomAfter(tz);
    // Chain a few occurrences: dispatch computes "next after the previous".
    for (let i = 0; i < 3; i++) {
      const next = add(cron, tz, after);
      if (next === null) break;
      after = Date.parse(next);
    }
  }
}
for (const tz of ["local", "Local", "", "Not/AZone"]) add("0 * * * *", tz, Date.UTC(2026, 0, 1));
while (cases.length < count) {
  const cron = randomCron();
  const tz = pick(timezones);
  let after = randomAfter(tz);
  const next = add(cron, tz, after);
  if (next !== null && rand() < 0.5) add(cron, tz, Date.parse(next));
}

writeFileSync(out, JSON.stringify({ openworkflow: version, cases }, null, 0).replaceAll("},{", "},\n{") + "\n");
console.log(`wrote ${cases.length} cases to ${out} (openworkflow ${version})`);
