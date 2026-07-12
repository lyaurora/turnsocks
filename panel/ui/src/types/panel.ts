export type ServiceInfo = {
  active: boolean;
  pid?: string;
};

export type TestMetric = {
  ok?: boolean;
  message?: string;
  avgMs?: number;
  minMs?: number;
  maxMs?: number;
  samples?: number;
  failed?: number;
};

export type TestCheck = {
  ok?: boolean;
  message?: string;
  ms?: number;
};

export type SpeedMetric = {
  ok?: boolean;
  message?: string;
  mbps?: number;
  bytes?: number;
  seconds?: number;
  threads?: number;
  source?: string;
};

export type ServerTest = {
  ok?: boolean;
  message?: string;
  addr?: string;
  tcpConnect?: TestMetric;
  socksUdp?: TestCheck;
  singleThread?: SpeedMetric;
  multiThread?: SpeedMetric;
  durationMs?: number;
  downloadBytes?: number;
  testedAt?: string;
};

export type ServerInfo = {
  raw: string;
  addr: string;
  username?: string;
  hasAuth: boolean;
  current: boolean;
  default: boolean;
  test?: ServerTest;
};

export type PanelState = {
  listen: string;
  doh: string;
  panelUsername: string;
  panelAuthEnabled: boolean;
  servers: ServerInfo[];
  service: ServiceInfo;
};

export type ApiResponse = {
  ok?: boolean;
  message?: string;
};

export type ConfigForm = {
  listen: string;
  doh: string;
  panelAuthEnabled: boolean;
  panelUsername: string;
  panelPassword: string;
};

export type ThemeMode = "light" | "system" | "dark";
