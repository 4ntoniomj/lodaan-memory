// Package skills embeds the lodan-memoria agent skill so the installer can deploy it.
package skills

import "embed"

// FS holds the lodan-memoria skill tree, rooted at "lodan-memoria".
//
//go:embed lodan-memoria
var FS embed.FS
