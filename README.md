# pagerduty-to-prometheus

Exports pagerduty incident stats as prometheus metrics.

## Problem to solve

We have lots of services.  Each services gets alerts routed to it via PagerDuty
and DataDog.  I want to make teams aware of general alert trends so they can work
to reduce the amount of time an incident is alerting.

## Solution

Scrape pagerduty for how frequently a service has active incidents.  Report that
as a percentage and challenge service owners to maintaine 100% incident free
services.

## Details

Every 1 minute, the service will scrape pager duty for
* Each service
* Recently triggered and updated incidents

Then, on a call to /metrics it will create an open window of 24 hours
and, for each incident, remove the parts of the window for the
incident betwen it's creation and last updated to resolved.  Then, it
reports the % of that window left as a metric.

It also reports all incidents that are currently in their ack or open
status, as well as all "resolved" (in the past 24 hours) incidents and
exports those.

## Setup

The service requires an environment variable PAGERDUTY_TOKEN to run.  You can
get this token [from PagerDuty](https://developer.pagerduty.com/docs/rest-api-v2/authentication/).


## Example metrics

In the example below, the `redis` service has had an active incident the entire 24 hour period, so it
has had 0% `incidents_free_percent`, while `gateway` had only a short incident

```
# HELP pdcollector_incidents_free_percent % time [0-1] of no incidents in this timerange
# TYPE pdcollector_incidents_free_percent gauge
pdcollector_incidents_free_percent{id="XXXXXX",service="redis",team="infra",timerange="24h0m0s"} 0
pdcollector_incidents_free_percent{id="YYYYYY",service="gateway",team="frontend",timerange="24h0m0s"} 0.9664720202523148
# HELP pdcollector_incidents_status_amount # of incidents in this timerange by their current status
# TYPE pdcollector_incidents_status_amount gauge
pdcollector_incidents_status_amount{id="XXXXXX",service="redis",status="acknowledged",team="infra",timerange="24h0m0s"} 0
pdcollector_incidents_status_amount{id="XXXXXX",service="redis",status="resolved",team="infra",timerange="24h0m0s"} 7
pdcollector_incidents_status_amount{id="XXXXXX",service="redis",status="triggered",team="infra",timerange="24h0m0s"} 14
pdcollector_incidents_status_amount{id="YYYYYY",service="gateway",status="acknowledged",team="frontend",timerange="24h0m0s"} 0
pdcollector_incidents_status_amount{id="YYYYYY",service="gateway",status="resolved",team="frontend",timerange="24h0m0s"} 7
pdcollector_incidents_status_amount{id="YYYYYY",service="gateway",status="triggered",team="frontend",timerange="24h0m0s"} 1
```

## Docker images

We publish docker images via github container repository.  Do not use the images
with `cache` in their name as they are build caches.  Instead, use the versioned images.

For example:
```
docker pull ghcr.io/cresta/pagerduty-to-prometheus:0.1.2
```

## Helm chart

A helm chart is included for easy kubernetes deployment.  Here is our example
flux CRD that shows how we configure it ourselves.  It also includes the annotations
we need for DataDog to scrape the metrics from it.  You'll also notice we put
our PagerDuty API token as an `envSecret` named `secret-env`.

```
---
apiVersion: helm.fluxcd.io/v1
kind: HelmRelease
metadata:
  name: pagerduty-to-prometheus
  namespace: pagerduty-to-prometheus
spec:
  chart:
    repository: https://cresta.github.io/pagerduty-to-prometheus
    name: pagerduty-to-prometheus
    version: 0.1.8
  values:
    podAnnotations:
      ad.datadoghq.com/pagerduty-to-prometheus.check_names: |
                      ["openmetrics"]
      ad.datadoghq.com/pagerduty-to-prometheus.init_configs: |
                      [{}]
      ad.datadoghq.com/pagerduty-to-prometheus.instances: |
          [
            {
              "prometheus_url": "http://%%host%%:8080/metrics",
              "namespace": "",
              "metrics": ["*"]
            }
          ]
    pd:
      logLevel: info
      envSecrets: secret-env
```

# Development

You'll need the environment variable PAGERDUTY_TOKEN to run the service.  After
placing it in your environment you can do the following.  For testing and
linting, you'll need to [install mage](https://magefile.org/).

## Running

```
go run ./cmd/pagerduty-to-prometheus/main.go
```

## Testing

```
mage go:test
```

## Linting

```
mage go:lint
```
