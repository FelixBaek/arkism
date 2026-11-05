package main

import (
	"os"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/memory"
)

// configuredServerLLM reads the server model: SKILL_LLM_ENDPOINT_URL (or
// SKILL_LLM_BASE_URL plus the protocol's path), SKILL_LLM_API_KEY, SKILL_LLM_MODEL,
// SKILL_LLM_PROTOCOL and the optional SKILL_LLM_PRESET (local, ollama, proxy,
// gateway). The gateway preset reaches this host's model gateway at
// ARKISM_GATEWAY_LISTEN with its local token (ARKISM_GATEWAY_TOKEN or the token
// file) when no key is set. Nothing is configured in recovery mode.
func configuredServerLLM(recovery bool) (memory.LLMConfig, error) {
	config := memory.LLMConfig{Endpoint: os.Getenv("SKILL_LLM_ENDPOINT_URL"), APIKey: os.Getenv("SKILL_LLM_API_KEY"), Model: os.Getenv("SKILL_LLM_MODEL"), Protocol: os.Getenv("SKILL_LLM_PROTOCOL")}
	preset, base := os.Getenv("SKILL_LLM_PRESET"), os.Getenv("SKILL_LLM_BASE_URL")
	if recovery || preset == "" && base == "" && config.APIKey == "" {
		return memory.LLMConfig{}, nil
	}
	if preset == "gateway" && config.APIKey == "" {
		token := os.Getenv("ARKISM_GATEWAY_TOKEN")
		if token == "" {
			var err error
			if token, err = host.ReadGatewayToken(host.GatewayTokenFile); err != nil {
				return config, err
			}
		}
		config.APIKey = token
	}
	return memory.ApplyLLMPreset(config, preset, base, "http://"+host.Env("ARKISM_GATEWAY_LISTEN", host.DefaultGatewayListen))
}
