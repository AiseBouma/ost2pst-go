package pst

import (
	"encoding/binary"
	"math"
	"unicode/utf16"
)

// Property is a single PC property or TC cell.
type Property struct {
	ID   PropID
	Type PropType
	HNID HNID
	Data []byte
}

const defaultHeapHNID = HNID(0x20) // HID(type HID, index 1, block 0)

func u16bytes(v uint16) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return b }
func u32bytes(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }
func u64bytes(v uint64) []byte { b := make([]byte, 8); binary.LittleEndian.PutUint64(b, v); return b }

// propFromHNID builds a property whose value lives in the HNID (fixed types <= 4 bytes).
func propFromHNID(id PropID, typ PropType, hnid HNID, defaultTime uint64) Property {
	p := Property{ID: id, Type: typ, HNID: hnid}
	switch typ {
	case PtypInteger16:
		p.Data = u16bytes(uint16(hnid))
	case PtypInteger32:
		p.Data = u32bytes(uint32(hnid))
	case PtypBoolean:
		p.Data = []byte{byte(hnid)}
	case PtypInteger64, PtypFloating64:
		p.Data = make([]byte, 8)
	case PtypTime:
		p.Data = u64bytes(defaultTime)
	}
	return p
}

// emptyProp creates a property without a value (used for required properties).
func emptyProp(id PropID, typ PropType, defaultTime uint64) Property {
	p := Property{ID: id, Type: typ}
	switch typ {
	case PtypInteger16:
		p.Data = make([]byte, 2)
	case PtypInteger32:
		p.Data = make([]byte, 4)
	case PtypBoolean:
		p.Data = make([]byte, 1)
	case PtypInteger64, PtypFloating64:
		p.Data = make([]byte, 8)
		p.HNID = defaultHeapHNID
	case PtypTime:
		p.Data = u64bytes(defaultTime)
		p.HNID = defaultHeapHNID
	}
	return p
}

func propBinary(id PropID, typ PropType, data []byte) Property {
	return Property{ID: id, Type: typ, HNID: defaultHeapHNID, Data: data}
}

func propU32(id PropID, v uint32) Property {
	return Property{ID: id, Type: PtypInteger32, HNID: HNID(v), Data: u32bytes(v)}
}

func propU64(id PropID, typ PropType, v uint64) Property {
	return Property{ID: id, Type: typ, HNID: defaultHeapHNID, Data: u64bytes(v)}
}

func propFloat(id PropID, v float64) Property {
	return propU64(id, PtypFloating64, math.Float64bits(v))
}

func propBool(id PropID, v bool) Property {
	if v {
		return Property{ID: id, Type: PtypBoolean, HNID: 1, Data: []byte{1}}
	}
	return Property{ID: id, Type: PtypBoolean, HNID: 0, Data: []byte{0}}
}

func propString(id PropID, s string) Property {
	p := Property{ID: id, Type: PtypString, HNID: defaultHeapHNID, Data: encodeUTF16(s)}
	if s == "" {
		p.HNID = 0
	}
	return p
}

func (p *Property) dataSize() int { return len(p.Data) }

func (p *Property) hasFixedSize() bool {
	switch p.Type {
	case PtypInteger16, PtypInteger32, PtypInteger64, PtypFloating64, PtypBoolean, PtypTime:
		return true
	}
	return false
}

func encodeUTF16(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, v := range u {
		binary.LittleEndian.PutUint16(b[2*i:], v)
	}
	return b
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

func findProp(props []Property, id PropID) int {
	for i := range props {
		if props[i].ID == id {
			return i
		}
	}
	return -1
}

func findPropTyped(props []Property, id PropID, typ PropType) int {
	for i := range props {
		if props[i].ID == id && props[i].Type == typ {
			return i
		}
	}
	return -1
}
