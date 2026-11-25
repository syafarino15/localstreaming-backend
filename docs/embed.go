// Package docs embeds the OpenAPI contract so the running server can always
// serve the exact specification it was built from, with no filesystem
// dependency and no external CDN.
package docs

import _ "embed"

// OpenAPI is the machine readable contract used by the CMS and TV code
// generators.
//
//go:embed openapi.yaml
var OpenAPI []byte
