package authorityprovider

import (
	"net"
	"time"

	"golang.org/x/sys/unix"
)

type controlConnection struct {
	conn   *net.UnixConn
	stream bool
}

func (connection controlConnection) write(payload, oob []byte) error {
	if !connection.stream {
		written, _, err := connection.conn.WriteMsgUnix(payload, oob, nil)
		if err != nil || written != len(payload) {
			return err
		}
		return nil
	}
	return writeStreamFrame(connection.conn, payload, oob)
}

func (connection controlConnection) read() ([]byte, []byte, int, error) {
	if !connection.stream {
		buffer := make([]byte, maxControlPacketBytes+1)
		oobBuffer := make([]byte, unix.CmsgSpace(maxControlPacketFDs*4))
		length, oobLength, flags, _, err := connection.conn.ReadMsgUnix(buffer, oobBuffer)
		return buffer[:length], oobBuffer[:oobLength], flags, err
	}
	return readStreamFrame(connection.conn)
}

func writeStreamFrame(connection *net.UnixConn, payload, oob []byte) error {
	if len(payload) == 0 || len(payload) > maxControlPacketBytes {
		return unix.EMSGSIZE
	}
	frame := make([]byte, 4+len(payload))
	frame[0] = byte(len(payload) >> 24)
	frame[1] = byte(len(payload) >> 16)
	frame[2] = byte(len(payload) >> 8)
	frame[3] = byte(len(payload))
	copy(frame[4:], payload)
	for len(frame) > 0 {
		written, sentOOB, err := connection.WriteMsgUnix(frame, oob, nil)
		if err != nil {
			return err
		}
		if sentOOB != len(oob) && len(oob) > 0 {
			return unix.EPROTO
		}
		if written <= 0 {
			return unix.EIO
		}
		frame = frame[written:]
		oob = nil
	}
	return nil
}

func readStreamFrame(connection *net.UnixConn) ([]byte, []byte, int, error) {
	deadline := time.Now().Add(30 * time.Second)
	_ = connection.SetReadDeadline(deadline)
	header := make([]byte, 4)
	oobBuffer := make([]byte, unix.CmsgSpace(maxControlPacketFDs*4))
	var receivedOOB []byte
	read := 0
	flags := 0
	for read < len(header) {
		chunk := make([]byte, len(header)-read)
		length, oobLength, readFlags, _, err := connection.ReadMsgUnix(chunk, oobBuffer)
		if err != nil {
			return nil, nil, 0, err
		}
		if readFlags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 {
			return nil, nil, readFlags, unix.EMSGSIZE
		}
		if length == 0 {
			return nil, nil, 0, unix.ECONNRESET
		}
		copy(header[read:], chunk[:length])
		read += length
		flags |= readFlags
		if oobLength > 0 {
			receivedOOB = append(receivedOOB, oobBuffer[:oobLength]...)
		}
	}
	length := int(header[0])<<24 | int(header[1])<<16 | int(header[2])<<8 | int(header[3])
	if length == 0 || length > maxControlPacketBytes {
		return nil, nil, flags, unix.EMSGSIZE
	}
	payload := make([]byte, length)
	read = 0
	for read < length {
		chunk := make([]byte, length-read)
		lengthRead, oobLength, readFlags, _, err := connection.ReadMsgUnix(chunk, oobBuffer)
		if err != nil {
			return nil, nil, 0, err
		}
		if readFlags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 {
			return nil, nil, readFlags, unix.EMSGSIZE
		}
		if lengthRead == 0 {
			return nil, nil, 0, unix.ECONNRESET
		}
		copy(payload[read:], chunk[:lengthRead])
		read += lengthRead
		flags |= readFlags
		if oobLength > 0 {
			receivedOOB = append(receivedOOB, oobBuffer[:oobLength]...)
		}
	}
	return payload, receivedOOB, flags, nil
}
