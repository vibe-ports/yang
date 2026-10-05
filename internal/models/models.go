// SPDX-License-Identifier: BSD-3-Clause

// Package models embeds libyang's internal modules (licences: NOTICE).
package models

import (
	"embed"
	"io/fs"
)

//go:embed libyang/*.yang
var files embed.FS

// Libyang is libyang v5.8.6's module directory (modules/): the internal
// modules every context loads, searchable like a search directory.
var Libyang, _ = fs.Sub(files, "libyang")
