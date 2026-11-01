package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"

	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

// This fixed offline hashing interface never reads schemas, files or commands
// named by a record. It uses the existing authoritative compiled codec.
func nativeHashStream(input io.Reader, output io.Writer) int {
	allowed := map[string]bool{
		"EventLogicalProjectionV2": true, "EvidenceBindingHashProjection": true,
		"ImportanceFeatureV1HashProjection": true, "StreamInitialPrefixProjectionV2": true,
		"StreamAcceptedStepProjectionV2": true, "DynamicInputManifestV2HashProjection": true,
		"P1InputManifestV1HashProjection": true, "DynamicsRunConfigV2HashProjection": true,
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxBytes)
	count := 0
	for scanner.Scan() {
		count++
		if count > 1000000 {
			return nativeHashFailure(output, problem("resource_limit", "$"))
		}
		name, payload, err := nativeHashRequest(scanner.Bytes())
		if err != nil {
			return nativeHashFailure(output, err)
		}
		if !allowed[name] {
			return nativeHashFailure(output, problem("unsupported_type", "$"))
		}
		value, err := dyn.DecodeStrict(payload, name)
		if err != nil {
			return nativeHashFailure(output, err)
		}
		sha, err := dyn.CanonicalSHA256(name, value)
		if err != nil {
			return nativeHashFailure(output, err)
		}
		if err := json.NewEncoder(output).Encode(map[string]string{"protocol": "exp02-native-hash-v1", "status": "hashed", "sha256": sha}); err != nil {
			return 2
		}
	}
	if scanner.Err() != nil || count == 0 {
		return nativeHashFailure(output, problem("invalid_json", "$"))
	}
	return 0
}

func nativeHashFailure(output io.Writer, err error) int {
	_ = json.NewEncoder(output).Encode(map[string]any{"protocol": "exp02-native-hash-v1", "status": "error", "error": fixedError(err)})
	return 2
}

func nativeHashRequest(raw []byte) (string, json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", nil, problem("invalid_json", "$")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || (name != "type_name" && name != "payload") {
			return "", nil, problem("invalid_value", "$")
		}
		if _, exists := fields[name]; exists {
			return "", nil, problem("duplicate_key", "$")
		}
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return "", nil, problem("invalid_json", "$")
		}
		fields[name] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || len(fields) != 2 {
		return "", nil, problem("invalid_json", "$")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return "", nil, problem("invalid_json", "$")
	}
	var name string
	if err = json.Unmarshal(fields["type_name"], &name); err != nil {
		return "", nil, problem("invalid_value", "$")
	}
	return name, fields["payload"], nil
}
