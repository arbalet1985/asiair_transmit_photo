// Package configs embeds the example configuration files.
package configs

import _ "embed"

//go:embed agent.example.yaml
var AgentExample []byte

//go:embed receiver.example.yaml
var ReceiverExample []byte
