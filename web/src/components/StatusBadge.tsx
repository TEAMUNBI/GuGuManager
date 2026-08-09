import { CircleCheck, CircleDashed, CircleOff, TriangleAlert } from "lucide-react";

interface Props {
  tone: "success" | "warning" | "danger" | "neutral";
  children: React.ReactNode;
  pulse?: boolean;
}

export function StatusBadge({ tone, children, pulse = false }: Props) {
  const Icon = tone === "success" ? CircleCheck : tone === "warning" ? TriangleAlert : tone === "danger" ? CircleOff : CircleDashed;
  return <span className={`status-badge status-${tone}${pulse ? " is-pulsing" : ""}`}><Icon size={13} strokeWidth={2.2} />{children}</span>;
}

export function toneForPower(value: string): Props["tone"] {
  if (value === "running") return "success";
  if (value === "starting" || value === "stopping") return "warning";
  if (value === "unhealthy") return "danger";
  return "neutral";
}

export function toneForNode(value: string): Props["tone"] {
  if (value === "available") return "success";
  if (value === "maintenance") return "warning";
  return "danger";
}

