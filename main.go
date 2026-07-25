// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Willem Mouton

// Command dnswizard is a configurable DNS proxy for lab and development work.
package main

import (
	"os"

	"github.com/joda32/dnswizard/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
