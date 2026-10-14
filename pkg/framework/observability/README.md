# `pkg/framework/observability`

Transport-neutral execution facts carried through command contexts. The
current `Trace` records bounded command, pipeline, reachability, environment,
and backend-reference metadata; products decide whether and how to report it.

Planned additions:
1. `logger.go` — factory on top of `log/slog`
2. `meter.go` — Counter / Histogram / Gauge + NDJSON meter
