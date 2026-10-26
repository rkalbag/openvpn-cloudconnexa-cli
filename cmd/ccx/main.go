// Command ccx is an unofficial CLI that adds Linux servers to CloudConnexa
// with one command on the laptop and one paste on the server.
package main

import (
	"os"

	"github.com/rkalbag/openvpn-cloudconnexa-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
