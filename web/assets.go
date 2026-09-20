// Package assets embeds labpower's templates and static files (CSS, the
// vendored htmx, self-hosted fonts) into the binary. It lives at the
// repository root's web/ directory (not internal/web, which holds the
// handler/middleware code) because Go's embed directive can only reach
// files at or below the embedding file's own directory.
package assets

import "embed"

//go:embed all:templates
var Templates embed.FS

//go:embed all:static
var Static embed.FS
