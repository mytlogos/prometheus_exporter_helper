# prometheus_exporter_helper

Helper package for creating prometheus exporter.
Depends on the experimental/wip package [exporter-toolkit](https://github.com/prometheus/exporter-toolkit).

Adds support for connecting to a [ziti](https://github.com/openziti/ziti) network.

For the version flag use the following ldflags:

```
-X github.com/prometheus/common/version.Version={{.Version}}
-X github.com/prometheus/common/version.Revision={{.Commit}}
-X github.com/prometheus/common/version.Branch={{.Branch}}
-X github.com/prometheus/common/version.BuildDate={{.Date}}
```
