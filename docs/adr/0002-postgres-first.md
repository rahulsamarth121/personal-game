# ADR 0002 — PostgreSQL-first, no Redis/Kafka/K8s

Date: 2026-09-25 · Status: accepted

## Context

Temptation to add queues/caches/orchestrators early.

## Decision

PostgreSQL from the start (migrations, pointer authority). No Redis,
Kafka, message broker, Kubernetes, or microservice split until a measured
need. Heartbeats/leases via Postgres + node channel.

## Consequences

- Simpler ops for a personal system; scale ceiling accepted.
- MinIO stands in for R2 in local integration tests.
