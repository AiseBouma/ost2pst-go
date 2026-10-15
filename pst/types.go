package pst

// NID helpers ([MS-PST] 2.2.2.1). A NID is a 32-bit value whose low 5 bits are the type.
func nidType(nid uint32) NidType { return NidType(nid & 0x1f) }
func nidIndex(nid uint32) uint32 { return nid >> 5 }
func makeNID(t NidType, index uint32) uint32 {
	return uint32(t)&0x1f | index<<5
}
func changeNidType(nid uint32, t NidType) uint32 { return makeNID(t, nidIndex(nid)) }

// HID is a heap id ([MS-PST] 2.3.1.1). The layout differs between the
// classic (PST) and 4K (OST) formats.
type HID uint32

func makeHID(t NidType, index, block uint16) HID {
	w1 := uint16(t) | uint16(index<<5)
	return HID(uint32(w1) | uint32(block)<<16)
}

func (h HID) typ() NidType { return NidType(h & 0x1f) }

func (h HID) index(is4K bool) uint16 {
	w1, w2 := uint16(h), uint16(h>>16)
	if is4K {
		return (w1 >> 5) | ((w2 & 0x0007) << 11)
	}
	return w1 >> 5
}

func (h HID) block(is4K bool) uint16 {
	w2 := uint16(h >> 16)
	if is4K {
		return w2 >> 3
	}
	return w2
}

// HNID is either a HID or a NID ([MS-PST] 2.3.3.2).
type HNID uint32

func (h HNID) hasValue() bool { return h != 0 }
func (h HNID) isHID() bool    { return h.hasValue() && NidType(h&0x1f) == NidHID }
func (h HNID) isNID() bool    { return !h.isHID() }

// BREF is a block reference.
type BREF struct {
	Bid uint64
	Ib  uint64
}

// NBTEntry is a node B-tree leaf entry (identical in OST and PST).
type NBTEntry struct {
	Nid       uint32
	BidData   uint64
	BidSub    uint64
	NidParent uint32
}

func (e NBTEntry) marshal(b []byte) {
	put32(b, 0, e.Nid)
	put32(b, 4, 0)
	put64(b, 8, e.BidData)
	put64(b, 16, e.BidSub)
	put32(b, 24, e.NidParent)
	put32(b, 28, 0)
}

func readNBTEntry(b []byte) NBTEntry {
	return NBTEntry{Nid: le32(b, 0), BidData: le64(b, 8), BidSub: le64(b, 16), NidParent: le32(b, 24)}
}

// BBTEntry is a PST block B-tree leaf entry (output file).
type BBTEntry struct {
	BREF
	Cb   uint16
	CRef uint16
}

func (e BBTEntry) marshal(b []byte) {
	put64(b, 0, e.Bid)
	put64(b, 8, e.Ib)
	put16(b, 16, e.Cb)
	put16(b, 18, e.CRef)
	put32(b, 20, 0)
}

// srcBlock is a block entry of the source file, combining OST and PST formats.
type srcBlock struct {
	Type       byte
	BREF       BREF
	CbStored   uint16
	CbInflated uint16
	CRef       uint16
	CLevel     int
	CEnt       int
	IcbTotal   uint32
}

// BTEntry is an intermediate B-tree page entry.
type BTEntry struct {
	Key  uint64
	BREF BREF
}

func (e BTEntry) marshal(b []byte) {
	put64(b, 0, e.Key)
	put64(b, 8, e.BREF.Bid)
	put64(b, 16, e.BREF.Ib)
}

// SLEntry is a subnode leaf entry ([MS-PST] 2.2.2.8.3.3.1.1).
type SLEntry struct {
	Nid     uint64
	BidData uint64
	BidSub  uint64
}

// Root is the ROOT structure inside the header ([MS-PST] 2.2.2.5).
type Root struct {
	Reserved  uint32
	FileEOF   uint64
	AMapLast  uint64
	AMapFree  uint64
	PMapFree  uint64
	NBT       BREF
	BBT       BREF
	AMapValid byte
	BReserved byte
	WReserved uint16
}

// Header is the Unicode file header ([MS-PST] 2.2.2.6), 564 bytes.
type Header struct {
	Magic          uint32
	CRCPartial     uint32
	MagicClient    uint16
	Ver            uint16
	VerClient      uint16
	PlatformCreate byte
	PlatformAccess byte
	Reserved1      uint32
	Reserved2      uint32
	BidUnused      uint64
	BidNextP       uint64
	Unique         uint32
	Rgnid          [32]uint32
	QwUnused       uint64
	Root           Root
	Align          uint32
	RgbFM          [128]byte
	RgbFP          [128]byte
	Sentinel       byte
	CryptMethod    byte
	RgbReserved    uint16
	BidNextB       uint64
	CRCFull        uint32
	RgbReserved2   uint32
	RgbReserved3   [32]byte
}

func (h *Header) marshal() []byte {
	b := make([]byte, HeaderSize)
	put32(b, 0, h.Magic)
	put32(b, 4, h.CRCPartial)
	put16(b, 8, h.MagicClient)
	put16(b, 10, h.Ver)
	put16(b, 12, h.VerClient)
	b[14] = h.PlatformCreate
	b[15] = h.PlatformAccess
	put32(b, 16, h.Reserved1)
	put32(b, 20, h.Reserved2)
	put64(b, 24, h.BidUnused)
	put64(b, 32, h.BidNextP)
	put32(b, 40, h.Unique)
	for i, v := range h.Rgnid {
		put32(b, 44+4*i, v)
	}
	put64(b, 172, h.QwUnused)
	r := &h.Root
	put32(b, 180, r.Reserved)
	put64(b, 184, r.FileEOF)
	put64(b, 192, r.AMapLast)
	put64(b, 200, r.AMapFree)
	put64(b, 208, r.PMapFree)
	put64(b, 216, r.NBT.Bid)
	put64(b, 224, r.NBT.Ib)
	put64(b, 232, r.BBT.Bid)
	put64(b, 240, r.BBT.Ib)
	b[248] = r.AMapValid
	b[249] = r.BReserved
	put16(b, 250, r.WReserved)
	put32(b, 252, h.Align)
	copy(b[256:384], h.RgbFM[:])
	copy(b[384:512], h.RgbFP[:])
	b[512] = h.Sentinel
	b[513] = h.CryptMethod
	put16(b, 514, h.RgbReserved)
	put64(b, 516, h.BidNextB)
	put32(b, 524, h.CRCFull)
	put32(b, 528, h.RgbReserved2)
	copy(b[532:564], h.RgbReserved3[:])
	return b
}

func unmarshalHeader(b []byte) Header {
	var h Header
	h.Magic = le32(b, 0)
	h.CRCPartial = le32(b, 4)
	h.MagicClient = le16(b, 8)
	h.Ver = le16(b, 10)
	h.VerClient = le16(b, 12)
	h.PlatformCreate = b[14]
	h.PlatformAccess = b[15]
	h.Reserved1 = le32(b, 16)
	h.Reserved2 = le32(b, 20)
	h.BidUnused = le64(b, 24)
	h.BidNextP = le64(b, 32)
	h.Unique = le32(b, 40)
	for i := range h.Rgnid {
		h.Rgnid[i] = le32(b, 44+4*i)
	}
	h.QwUnused = le64(b, 172)
	r := &h.Root
	r.Reserved = le32(b, 180)
	r.FileEOF = le64(b, 184)
	r.AMapLast = le64(b, 192)
	r.AMapFree = le64(b, 200)
	r.PMapFree = le64(b, 208)
	r.NBT = BREF{Bid: le64(b, 216), Ib: le64(b, 224)}
	r.BBT = BREF{Bid: le64(b, 232), Ib: le64(b, 240)}
	r.AMapValid = b[248]
	r.BReserved = b[249]
	r.WReserved = le16(b, 250)
	h.Align = le32(b, 252)
	copy(h.RgbFM[:], b[256:384])
	copy(h.RgbFP[:], b[384:512])
	h.Sentinel = b[512]
	h.CryptMethod = b[513]
	h.RgbReserved = le16(b, 514)
	h.BidNextB = le64(b, 516)
	h.CRCFull = le32(b, 524)
	h.RgbReserved2 = le32(b, 528)
	copy(h.RgbReserved3[:], b[532:564])
	return h
}
