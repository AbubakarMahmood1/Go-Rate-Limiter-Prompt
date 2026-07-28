# REGRIND TRIGGERS

Open a new source era only when at least one objective condition occurs:

1. A test, incident, or reproducible report shows over-admission, partial bulk consumption, status mutation, unsafe retry timing, or memory/Redis decision divergence.
2. A supported Go, Redis, Gin, Prometheus, or Grafana upgrade breaks the committed verification gate or removes a required security fix path.
3. The intended deployment genuinely requires Redis Sentinel or Cluster; that topology must receive its own client design, scripts/key-slot analysis, failure tests, and claims review.
4. A measured, documented workload demonstrates that the current algorithm/store path is a material bottleneck and includes a repeatable benchmark/profile.
5. A versioned API compatibility requirement emerges from a real consumer.
6. The reset or policy-override boundary moves from a trusted internal network to an untrusted/public surface.
7. A security report identifies a credible vulnerability in parsing, key isolation, secret handling, dependency behavior, or denial-of-service resistance.
8. CI no longer executes every required Redis/image/Compose gate or begins skipping them silently.

Do not regrind merely to add technologies, another algorithm, cosmetic infrastructure, or unmeasured optimization.
