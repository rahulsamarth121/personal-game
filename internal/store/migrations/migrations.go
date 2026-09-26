package migrations

import _ "embed"

// SQL0001 is the base schema (idempotent). Embedded so the control plane
// migrates without depending on the working directory at runtime.
//
//go:embed 0001_init.sql
var SQL0001 string
