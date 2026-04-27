package main

import (
	"os"

	scaleway "github.com/codecentric/fleeting-plugin-scaleway"
	"gitlab.com/gitlab-org/fleeting/fleeting/plugin"
)

var cliCommands = map[string]bool{
	"list":         true,
	"create":       true,
	"delete":       true,
	"connect-info": true,
	"update":       true,
	"bootstrap":    true,
	"help":         true,
	"--help":       true,
	"-h":           true,
}

func main() {
	if len(os.Args) > 1 && cliCommands[os.Args[1]] {
		runCLI(os.Args[1:])
		return
	}
	plugin.Main(&scaleway.InstanceGroup{}, scaleway.Version)
}
