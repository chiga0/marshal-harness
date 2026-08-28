package processsupervisor

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFrameRoundTripUsesFrozenGrammar(t *testing.T) {
	frame, err := encodeFrame(struct {
		B int `json:"b"`
		A int `json:"a"`
	}{B: 2, A: 1}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(frame) != "0000000d:{\"a\":1,\"b\":2}\n" {
		t.Fatalf("frame = %q", frame)
	}
	payload, err := readFrame(bufio.NewReader(bytes.NewReader(frame)), 1024)
	if err != nil || string(payload) != `{"a":1,"b":2}` {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}

func TestFrameRejectsAlternateOrHostileEncodings(t *testing.T) {
	valid := []byte("00000007:{\"a\":1}\n")
	for name, input := range map[string][]byte{
		"uppercase length": []byte("0000000A:{\"a\":1234}\n"),
		"whitespace":       []byte("00000008:{ \"a\":1}\n"),
		"duplicate":        []byte("0000000d:{\"a\":1,\"a\":2}\n"),
		"missing newline":  valid[:len(valid)-1],
		"wrong delimiter":  []byte("00000007;{\"a\":1}\n"),
		"oversize":         []byte("00000401:{\"a\":1}\n"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readFrame(bufio.NewReader(bytes.NewReader(input)), 1024)
			if err == nil {
				t.Fatal("hostile frame admitted")
			}
		})
	}
	if _, err := readFrame(bufio.NewReader(strings.NewReader("")), 1024); !errors.Is(err, io.EOF) {
		t.Fatalf("empty error = %v", err)
	}
}
