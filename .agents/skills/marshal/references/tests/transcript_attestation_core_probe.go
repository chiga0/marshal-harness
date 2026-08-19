package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/chiga0/marshal-harness/internal/adapter/qoder"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "--digest-json" {
		raw, err := os.ReadFile(os.Args[2])
		if err != nil {
			fail("digest-read-failed")
		}
		digest, err := canonical.DigestJSON(raw)
		if err != nil {
			fail("digest-json-invalid")
		}
		fmt.Println(digest)
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	decoder.DisallowUnknownFields()
	var input qoder.TranscriptAttestationInput
	if err := decoder.Decode(&input); err != nil {
		fail("checker-input-invalid")
	}
	if decoder.Decode(&struct{}{}) == nil {
		fail("checker-input-trailing")
	}
	observation, err := qoder.ValidateTranscriptAttestation(input)
	if err != nil {
		fail(err.Error())
	}
	output := struct {
		Status      string                                 `json:"status"`
		ReasonCode  string                                 `json:"reasonCode"`
		Identity    map[string]string                      `json:"identity"`
		Observation qoder.TranscriptAttestationObservation `json:"observation"`
	}{Status: "pass", ReasonCode: "transcript-attestation-pass", Identity: qoder.TranscriptAttestationImplementationIdentity(), Observation: observation}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(output); err != nil {
		os.Exit(1)
	}
}

func fail(reason string) {
	fmt.Fprintf(os.Stderr, "{\"status\":\"fail\",\"reasonCode\":%q}\n", reason)
	os.Exit(1)
}
