package protocol

import (
	"testing"
	"bytes"
)

func TestSerializeTunnelMessage(t *testing.T) {
	msg := TunnelMessage{
		ID:      123,
		Type:    MSG_TYPE_HTTP_REQ,
		Payload: []byte("test payload"),
	}
	
	data, err := SerializeTunnelMessage(msg)
	if err != nil {
		t.Fatalf("Failed to serialize message: %v", err)
	}
	
	if len(data) < 9 {
		t.Error("Serialized data too short")
	}
}

func TestDeserializeTunnelMessage(t *testing.T) {
	// 先序列化一个消息
	original := TunnelMessage{
		ID:      456,
		Type:    MSG_TYPE_HTTP_RES,
		Payload: []byte("response data"),
	}
	
	data, err := SerializeTunnelMessage(original)
	if err != nil {
		t.Fatalf("Failed to serialize: %v", err)
	}
	
	// 再反序列化
	deserialized, err := DeserializeTunnelMessage(data)
	if err != nil {
		t.Fatalf("Failed to deserialize: %v", err)
	}
	
	// 验证数据一致性
	if deserialized.ID != original.ID {
		t.Errorf("ID mismatch: expected %d, got %d", original.ID, deserialized.ID)
	}
	
	if deserialized.Type != original.Type {
		t.Errorf("Type mismatch: expected %d, got %d", original.Type, deserialized.Type)
	}
	
	if !bytes.Equal(deserialized.Payload, original.Payload) {
		t.Error("Payload mismatch")
	}
}

func TestDeserializeTunnelMessageTooShort(t *testing.T) {
	// 测试数据太短的情况
	shortData := []byte{1, 2, 3}
	
	_, err := DeserializeTunnelMessage(shortData)
	if err == nil {
		t.Error("Expected error for short data")
	}
}

func TestMessageTypes(t *testing.T) {
	// 验证消息类型常量
	if MSG_TYPE_HTTP_REQ != 1 {
		t.Errorf("Expected MSG_TYPE_HTTP_REQ to be 1, got %d", MSG_TYPE_HTTP_REQ)
	}
	
	if MSG_TYPE_HTTP_RES != 2 {
		t.Errorf("Expected MSG_TYPE_HTTP_RES to be 2, got %d", MSG_TYPE_HTTP_RES)
	}
	
	if MSG_TYPE_HTTP_RES_CHUNK != 3 {
		t.Errorf("Expected MSG_TYPE_HTTP_RES_CHUNK to be 3, got %d", MSG_TYPE_HTTP_RES_CHUNK)
	}
}