export interface TimezoneGroup {
  region: string;
  zones: string[];
}

export const COMMON_TIMEZONE_GROUPS: TimezoneGroup[] = [
  {
    region: "Australia / New Zealand",
    zones: [
      "Australia/Sydney",
      "Australia/Melbourne",
      "Australia/Brisbane",
      "Australia/Adelaide",
      "Australia/Perth",
      "Australia/Hobart",
      "Pacific/Auckland",
    ],
  },
  {
    region: "Asia",
    zones: [
      "Asia/Singapore",
      "Asia/Tokyo",
      "Asia/Seoul",
      "Asia/Hong_Kong",
      "Asia/Shanghai",
      "Asia/Bangkok",
      "Asia/Kolkata",
      "Asia/Dubai",
    ],
  },
  {
    region: "Europe",
    zones: [
      "Europe/London",
      "Europe/Dublin",
      "Europe/Paris",
      "Europe/Berlin",
      "Europe/Amsterdam",
      "Europe/Madrid",
      "Europe/Rome",
      "Europe/Zurich",
      "Europe/Athens",
      "Europe/Helsinki",
    ],
  },
  {
    region: "Americas",
    zones: [
      "America/New_York",
      "America/Toronto",
      "America/Chicago",
      "America/Denver",
      "America/Phoenix",
      "America/Los_Angeles",
      "America/Vancouver",
      "America/Sao_Paulo",
      "America/Mexico_City",
      "America/Bogota",
    ],
  },
  {
    region: "Africa / Middle East",
    zones: [
      "Africa/Johannesburg",
      "Africa/Cairo",
      "Africa/Nairobi",
      "Asia/Jerusalem",
      "Asia/Riyadh",
      "Asia/Qatar",
    ],
  },
  {
    region: "UTC / Etc",
    zones: ["UTC", "Etc/UTC", "Etc/GMT"],
  },
];

export function formatHour(hour: number): string {
  if (hour === 24) {
    return "24:00";
  }
  return `${String(hour).padStart(2, "0")}:00`;
}
