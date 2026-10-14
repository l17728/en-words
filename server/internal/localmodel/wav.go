package localmodel

import (
	"encoding/binary"
	"fmt"
)

// validateWAV verifies the entire container, not intelligibility or loudness.
func validateWAV(raw []byte) error {
	bad := func() error { return fmt.Errorf("model console: invalid WAV") }
	if len(raw) < 44 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WAVE" || uint64(binary.LittleEndian.Uint32(raw[4:8]))+8 != uint64(len(raw)) {
		return bad()
	}
	fmtSeen, dataSeen := false, false
	var align uint16
	var audioSize uint32
	for offset := 12; offset < len(raw); {
		if len(raw)-offset < 8 {
			return bad()
		}
		size := uint64(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
		end := uint64(offset) + 8 + size
		if end > uint64(len(raw)) {
			return bad()
		}
		body := raw[offset+8 : int(end)]
		switch string(raw[offset : offset+4]) {
		case "fmt ":
			if fmtSeen || len(body) < 16 {
				return bad()
			}
			fmtSeen = true
			format := binary.LittleEndian.Uint16(body)
			channels := binary.LittleEndian.Uint16(body[2:])
			rate := binary.LittleEndian.Uint32(body[4:])
			byteRate := binary.LittleEndian.Uint32(body[8:])
			align = binary.LittleEndian.Uint16(body[12:])
			bits := binary.LittleEndian.Uint16(body[14:])
			if channels < 1 || channels > 8 || rate < 8000 || rate > 192000 || bits%8 != 0 {
				return bad()
			}
			if (format == 1 && bits != 8 && bits != 16 && bits != 24 && bits != 32) || (format == 3 && bits != 32 && bits != 64) || (format != 1 && format != 3) {
				return bad()
			}
			if align != channels*(bits/8) || uint64(byteRate) != uint64(rate)*uint64(align) {
				return bad()
			}
		case "data":
			if dataSeen || size == 0 {
				return bad()
			}
			dataSeen = true
			audioSize = uint32(size)
		}
		end += size % 2
		if end > uint64(len(raw)) {
			return bad()
		}
		offset = int(end)
	}
	if !fmtSeen || !dataSeen || align == 0 || audioSize%uint32(align) != 0 {
		return bad()
	}
	return nil
}
