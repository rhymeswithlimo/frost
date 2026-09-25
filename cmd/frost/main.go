// Command frost is an encrypted, incremental backup tool.
package main

import (
	"os"

	"github.com/rhymeswithlimo/frost/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
