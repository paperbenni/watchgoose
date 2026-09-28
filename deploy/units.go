// Package deploy embeds the systemd units used by the setup wizard.
package deploy

import _ "embed"

//go:embed watchgoose.service
var ListenerUnit []byte

//go:embed watchgoose-poke@.service
var PokeUnit []byte
