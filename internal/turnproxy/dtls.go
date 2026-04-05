// Package turnproxy реализует TCP→UDP туннель с маскировкой под DTLS 1.2.
//
// Схема трафика:
//
//	sing-box → TCP:9000 → [turnproxy] → UDP/DTLS → relay-сервер → VLESS backend
//
// DPI видит UDP датаграммы с DTLS 1.2 ApplicationData record header —
// идентично WebRTC трафику VK видеозвонка.
package turnproxy

import (
	"encoding/binary"
	"fmt"
	"io"
)

// DTLS 1.2 constants (RFC 6347).
const (
	dtlsContentTypeAppData = 23    // application_data
	dtlsVersionMajor       = 0xFE  // DTLS 1.2 major
	dtlsVersionMinor       = 0xFD  // DTLS 1.2 minor
	dtlsEpoch              = 1     // epoch 1 — «после handshake»
	dtlsHeaderLen          = 13    // content(1)+version(2)+epoch(2)+seq(6)+length(2)
)

// innerHeaderLen — размер внутреннего заголовка внутри DTLS payload.
// stream_id(4) + seq(4) + flags(1) = 9 байт.
const innerHeaderLen = 9

// Flags для внутреннего заголовка.
const (
	FlagSYN  byte = 0x01 // открытие нового потока
	FlagFIN  byte = 0x02 // закрытие потока
	FlagDATA byte = 0x00 // данные
	FlagPING byte = 0x04 // keepalive ping
	FlagPONG byte = 0x08 // keepalive pong
)

// MaxChunkSize — максимальный размер данных в одном датаграмме.
// 1200 байт ≈ стандартный WebRTC DTLS payload, умещается в один IP пакет.
const MaxChunkSize = 1200

// encodeDTLS оборачивает inner payload в DTLS 1.2 ApplicationData record.
// dtlsSeq — монотонно растущий DTLS sequence number (6 байт, big-endian).
func encodeDTLS(payload []byte, dtlsSeq uint64) []byte {
	length := uint16(len(payload))
	out := make([]byte, dtlsHeaderLen+len(payload))
	out[0] = dtlsContentTypeAppData
	out[1] = dtlsVersionMajor
	out[2] = dtlsVersionMinor
	binary.BigEndian.PutUint16(out[3:5], dtlsEpoch)
	// 6-байтный sequence number: старшие 2 байта нулевые, младшие 4 — из dtlsSeq.
	// В реальном DTLS seq 48-бит. Нам хватит 32 бит для миллиардов пакетов.
	binary.BigEndian.PutUint16(out[5:7], uint16(dtlsSeq>>32))
	binary.BigEndian.PutUint32(out[7:11], uint32(dtlsSeq))
	binary.BigEndian.PutUint16(out[11:13], length)
	copy(out[13:], payload)
	return out
}

// decodeDTLS проверяет DTLS заголовок и возвращает payload.
// Возвращает ошибку если заголовок невалиден.
func decodeDTLS(data []byte) (payload []byte, err error) {
	if len(data) < dtlsHeaderLen {
		return nil, fmt.Errorf("datagram too short: %d < %d", len(data), dtlsHeaderLen)
	}
	if data[0] != dtlsContentTypeAppData {
		return nil, fmt.Errorf("unexpected content type: %d", data[0])
	}
	if data[1] != dtlsVersionMajor || data[2] != dtlsVersionMinor {
		return nil, fmt.Errorf("unexpected DTLS version: %02x%02x", data[1], data[2])
	}
	length := binary.BigEndian.Uint16(data[11:13])
	if int(length) > len(data)-dtlsHeaderLen {
		return nil, fmt.Errorf("declared length %d > available %d", length, len(data)-dtlsHeaderLen)
	}
	return data[dtlsHeaderLen : dtlsHeaderLen+int(length)], nil
}

// innerFrame — разобранный внутренний заголовок.
type innerFrame struct {
	streamID uint32
	seq      uint32
	flags    byte
	data     []byte
}

// encodeInner кодирует innerFrame в байты (без DTLS обёртки).
func encodeInner(f innerFrame) []byte {
	out := make([]byte, innerHeaderLen+len(f.data))
	binary.BigEndian.PutUint32(out[0:4], f.streamID)
	binary.BigEndian.PutUint32(out[4:8], f.seq)
	out[8] = f.flags
	copy(out[9:], f.data)
	return out
}

// decodeInner разбирает innerFrame из байт.
func decodeInner(b []byte) (innerFrame, error) {
	if len(b) < innerHeaderLen {
		return innerFrame{}, io.ErrUnexpectedEOF
	}
	f := innerFrame{
		streamID: binary.BigEndian.Uint32(b[0:4]),
		seq:      binary.BigEndian.Uint32(b[4:8]),
		flags:    b[8],
		data:     b[innerHeaderLen:],
	}
	return f, nil
}
