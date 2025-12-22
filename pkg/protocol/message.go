package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// 消息类型常量
const (
	MSG_TYPE_HTTP_REQ       = 1
	MSG_TYPE_HTTP_RES       = 2
	MSG_TYPE_HTTP_RES_CHUNK = 3
)

// TunnelMessage 定义了隧道中传输的消息格式
type TunnelMessage struct {
	ID      uint64
	Type    uint8
	Payload []byte
}

// SerializeTunnelMessage 序列化隧道消息
func SerializeTunnelMessage(msg TunnelMessage) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.BigEndian, msg.ID); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, msg.Type); err != nil {
		return nil, err
	}
	if _, err := buf.Write(msg.Payload); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DeserializeTunnelMessage 反序列化隧道消息
func DeserializeTunnelMessage(data []byte) (TunnelMessage, error) {
	if len(data) < 9 { // 8 bytes ID + 1 byte Type
		return TunnelMessage{}, errors.New("message too short")
	}
	msg := TunnelMessage{
		ID:   binary.BigEndian.Uint64(data[:8]),
		Type: data[8],
	}
	msg.Payload = data[9:]
	return msg, nil
}