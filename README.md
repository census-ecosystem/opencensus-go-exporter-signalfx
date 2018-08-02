# OpenCensus SignalFx Stats Exporter for Go

The _OpenCensus SignalFx Stats Exporter for Go_ is a stats exporter that
exports data to [SignalFx](https://signalfx.com), a real-time monitoring
solution for cloud and distributed applications. SignalFx ingests that
data and offers various visualizations on charts, dashboards and service
maps, as well as real-time anomaly detection.

## Quickstart

### Import

```
import "opencensus-go-signalfx/exporter/signalfx"
```

The API of this project is still evolving, see: [Deprecation Policy](#deprecation-policy).
The use of vendoring or a dependency management tool is recommended.


### Prerequisites

To use this exporter, you must have a [SignalFx](https://signalfx.com)
account and corresponding [data ingest
token](https://docs.signalfx.com/en/latest/admin-guide/tokens.html).

OpenCensus Go libraries require Go 1.8 or later.

### Register the exporter

```go
func main() {
    // SignalFx token is part of the Options struct.
    // Stats will be reported every second by default.
    exporter, err := signalfx.NewExporter(signalfx.Options{Token: "token"})
    ...
}
```

If you want to set a different reporting interval (5 seconds, for example), use:

```go
func main() {
    exporter, err := signalfx.NewExporter(signalfx.Options{Token: "token"})
    view.RegisterExporter(exporter)
    ....
    view.SetReportingPeriod(5 * time.Second)
}
```# OpenCensus SignalFx Stats Exporter for Go
