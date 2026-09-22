// Package jobv1 embeds the published Job request schema for MCP discovery.
package jobv1

import _ "embed"

//go:embed job.schema.json
var Schema []byte
