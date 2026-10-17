package graphv1

import _ "embed"

// Schema describes the versioned IU graph submission contract.
//
//go:embed graph.schema.json
var Schema []byte
