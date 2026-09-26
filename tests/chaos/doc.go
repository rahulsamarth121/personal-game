// Package chaos reserves deterministic failure tests (Stage 4), especially:
// node A gen 41 dies -> node B gen 42 -> A returns and must be fenced so
// generation 42 remains latest. See docs/architecture for the full matrix.
package chaos
