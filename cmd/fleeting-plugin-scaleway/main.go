package main

import (
	scaleway "github.com/codecentric/fleeting-plugin-scaleway"
	"gitlab.com/gitlab-org/fleeting/fleeting/plugin"
)

func main() {
	plugin.Main(&scaleway.InstanceGroup{}, scaleway.Version)
}
