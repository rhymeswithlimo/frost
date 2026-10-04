// Command frost is an encrypted, incremental backup tool.
package main

import (
	"os"

	_ "github.com/rhymeswithlimo/frost/cmd/frost/winres" // frost.exe's version resource, once generated
	"github.com/rhymeswithlimo/frost/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
