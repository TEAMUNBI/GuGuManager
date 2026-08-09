interface Props {
  values: number[];
  tone?: "mint" | "orange" | "blue";
  label: string;
}

export function MetricBars({ values, tone = "mint", label }: Props) {
  const max = Math.max(...values, 1);
  return (
    <div className={`metric-bars bars-${tone}`} role="img" aria-label={label}>
      {values.map((value, index) => <span key={index} style={{ height: `${Math.max(8, (value / max) * 100)}%` }} />)}
    </div>
  );
}

