package processsupervisor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const frameHeaderBytes = 9

// canonicalValue produces the sole admitted representation for protocol and
// journal objects. Callers must never send json.Marshal output directly.
func canonicalValue(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalid
	}
	result, err := canonical.JSON(raw)
	if err != nil {
		return nil, ErrInvalid
	}
	return result, nil
}

func digestValue(value any) (string, error) {
	raw, err := canonicalValue(value)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(raw), nil
}

func strictCanonicalDecode(data []byte, target any) error {
	if len(data) == 0 {
		return ErrInvalid
	}
	admitted, err := canonical.JSON(data)
	if err != nil || !bytes.Equal(admitted, data) {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalid
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func encodeFrame(value any, limit int) ([]byte, error) {
	payload, err := canonicalValue(value)
	if err != nil || len(payload) == 0 || len(payload) > limit {
		return nil, ErrInvalid
	}
	frame := make([]byte, 0, frameHeaderBytes+len(payload)+1)
	frame = fmt.Appendf(frame, "%08x:", len(payload))
	frame = append(frame, payload...)
	frame = append(frame, '\n')
	return frame, nil
}

func writeFrame(writer io.Writer, value any, limit int) error {
	frame, err := encodeFrame(value, limit)
	if err != nil {
		return err
	}
	for len(frame) > 0 {
		written, err := writer.Write(frame)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(frame) {
			return io.ErrShortWrite
		}
		frame = frame[written:]
	}
	return nil
}

func readFrame(reader *bufio.Reader, limit int) ([]byte, error) {
	if reader == nil || limit <= 0 {
		return nil, ErrInvalid
	}
	header := make([]byte, frameHeaderBytes)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	if header[8] != ':' {
		return nil, ErrInvalid
	}
	for _, value := range header[:8] {
		if !lowerHex(value) {
			return nil, ErrInvalid
		}
	}
	length64, err := strconv.ParseUint(string(header[:8]), 16, 32)
	if err != nil || length64 == 0 || length64 > uint64(limit) {
		return nil, ErrInvalid
	}
	payload := make([]byte, int(length64))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	terminator, err := reader.ReadByte()
	if err != nil || terminator != '\n' {
		return nil, ErrInvalid
	}
	admitted, err := canonical.JSON(payload)
	if err != nil || !bytes.Equal(admitted, payload) {
		return nil, ErrInvalid
	}
	return payload, nil
}

func lowerHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f'
}
