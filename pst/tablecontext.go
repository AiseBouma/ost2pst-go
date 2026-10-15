package pst

import (
	"fmt"
	"sort"
)

// tcInfo is TCINFO ([MS-PST] 2.3.4.1), 22 bytes.
type tcInfo struct {
	btype       byte
	cCols       byte
	rgib4b      uint16
	rgib2b      uint16
	rgib1b      uint16
	rgibBM      uint16
	hidRowIndex HID
	hnidRows    HNID
	hidIndex    HID
}

func (t *tcInfo) marshal(b []byte) {
	b[0] = t.btype
	b[1] = t.cCols
	put16(b, 2, t.rgib4b)
	put16(b, 4, t.rgib2b)
	put16(b, 6, t.rgib1b)
	put16(b, 8, t.rgibBM)
	put32(b, 10, uint32(t.hidRowIndex))
	put32(b, 14, uint32(t.hnidRows))
	put32(b, 18, uint32(t.hidIndex))
}

func readTCInfo(b []byte) tcInfo {
	return tcInfo{
		btype: b[0], cCols: b[1],
		rgib4b: le16(b, 2), rgib2b: le16(b, 4), rgib1b: le16(b, 6), rgibBM: le16(b, 8),
		hidRowIndex: HID(le32(b, 10)), hnidRows: HNID(le32(b, 14)), hidIndex: HID(le32(b, 18)),
	}
}

// colDesc is TCOLDESC ([MS-PST] 2.3.4.2), 8 bytes.
type colDesc struct {
	tag    uint32
	ibData uint16
	cbData byte
	iBit   byte
}

func (c *colDesc) propID() PropID     { return PropID(c.tag >> 16) }
func (c *colDesc) propType() PropType { return PropType(c.tag & 0xffff) }

func makeTag(id PropID, typ PropType) uint32 { return uint32(id)<<16 | uint32(typ) }

var (
	tagLtpRowID  = makeTag(PidTagLtpRowId, PtypInteger32)
	tagLtpRowVer = makeTag(PidTagLtpRowVer, PtypInteger32)
)

type tcRowID struct {
	rowID    uint32
	rowIndex uint32
}

// rowData is one row of the row matrix.
type rowData struct {
	props []Property
	ceb   []byte
	data  []byte
}

func (r *rowData) bytes() []byte {
	out := make([]byte, 0, len(r.data)+len(r.ceb))
	out = append(out, r.data...)
	return append(out, r.ceb...)
}

func newRowData(info *tcInfo, rowID uint32, props []Property, uid func() uint32) *rowData {
	r := &rowData{
		data:  make([]byte, info.rgib1b),
		ceb:   make([]byte, int(info.rgibBM)-int(info.rgib1b)),
		props: props,
	}
	put32(r.data, 0, rowID)
	put32(r.data, 4, uid())
	setBit(r.ceb, 0)
	setBit(r.ceb, 1)
	return r
}

// TableContext is an in-memory Table Context ([MS-PST] 2.3.4).
type TableContext struct {
	info       tcInfo
	cols       []*colDesc
	rowIndexes []tcRowID
	rows       []*rowData
	uid        func() uint32 // source of row version values (header dwUnique)
}

func newTableContext(uid func() uint32) *TableContext {
	return &TableContext{
		info: tcInfo{
			btype:       bTypeTC,
			hidRowIndex: makeHID(NidHID, 1, 0), // Outlook differs from MS-PST
		},
		uid: uid,
	}
}

// clone is a shallow copy (columns are shared, like the C# MemberwiseClone).
func (tc *TableContext) clone() *TableContext {
	c := *tc
	return &c
}

func (tc *TableContext) resetRows() {
	tc.rowIndexes = nil
	tc.rows = nil
}

func (tc *TableContext) rowMatrixSize() int { return len(tc.rows) * int(tc.info.rgibBM) }

func propTypeSize(t PropType) byte {
	switch t {
	case PtypInteger16:
		return 2
	case PtypInteger32:
		return 4
	case PtypInteger64, PtypFloating64, PtypTime:
		return 8
	case PtypBoolean:
		return 1
	}
	return 0
}

func (tc *TableContext) findColumn(id PropID, typ PropType) int {
	tag := makeTag(id, typ)
	for i, c := range tc.cols {
		if c.tag == tag {
			return i
		}
	}
	return -1
}

func (tc *TableContext) addColumn(id PropID, typ PropType) {
	if tc.findColumn(id, typ) >= 0 {
		panic(fmt.Sprintf("column %#x already defined", id))
	}
	cb := propTypeSize(typ)
	if cb == 0 {
		cb = 4 // variable size: the cell holds a HNID
	}
	tc.cols = append(tc.cols, &colDesc{tag: makeTag(id, typ), cbData: cb})
	tc.info.cCols++
	tc.sortColumns()
	tc.rebuildRows(-1)
}

func (tc *TableContext) removeColumn(id PropID, typ PropType) {
	if ix := tc.findColumn(id, typ); ix >= 0 {
		tc.cols = append(tc.cols[:ix:ix], tc.cols[ix+1:]...)
		tc.info.cCols--
		tc.sortColumns()
	}
	tc.rebuildRows(int(id))
}

// rebuildRows recreates every row with the current layout; removeID >= 0 also
// drops that property from the rows.
func (tc *TableContext) rebuildRows(removeID int) {
	for i := range tc.rows {
		props := tc.rows[i].props
		if removeID >= 0 {
			if ix := findProp(props, PropID(removeID)); ix >= 0 {
				props = append(props[:ix:ix], props[ix+1:]...)
			}
		}
		tc.rows[i] = newRowData(&tc.info, tc.rowIndexes[i].rowID, props, tc.uid)
	}
}

func (tc *TableContext) addRow(r tcRowID, props []Property) {
	tc.rows = append(tc.rows, newRowData(&tc.info, r.rowID, props, tc.uid))
}

func (tc *TableContext) addRowNID(nid uint32, props []Property) {
	tc.rowIndexes = append(tc.rowIndexes, tcRowID{rowID: nid, rowIndex: uint32(len(tc.rowIndexes))})
	sort.SliceStable(tc.rowIndexes, func(a, b int) bool { return tc.rowIndexes[a].rowID < tc.rowIndexes[b].rowID })
	tc.rows = append(tc.rows, newRowData(&tc.info, nid, props, tc.uid))
}

// rowMatrixBlocks packs the rows into data blocks of at most MaxDatablockSize.
func (tc *TableContext) rowMatrixBlocks() [][]byte {
	var blocks [][]byte
	rowSize := int(tc.info.rgibBM)
	cur := []byte{}
	for _, r := range tc.rows {
		if len(cur)+rowSize > MaxDatablockSize {
			full := make([]byte, MaxDatablockSize)
			copy(full, cur)
			blocks = append(blocks, full)
			cur = []byte{}
		}
		cur = append(cur, r.bytes()[:rowSize]...)
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks
}

// header serialises TCINFO + column descriptors.
func (tc *TableContext) header() []byte {
	b := make([]byte, tcInfoSize+tcColDescSize*len(tc.cols))
	tc.info.marshal(b)
	for i, c := range tc.cols {
		if c.tag == tagLtpRowID || c.tag == tagLtpRowVer {
			c.cbData = 4
		}
		o := tcInfoSize + tcColDescSize*i
		put32(b, o, c.tag)
		put16(b, o+4, c.ibData)
		b[o+6] = c.cbData
		b[o+7] = c.iBit
	}
	return b
}

func (tc *TableContext) sortColumns() {
	ibData := uint16(8)
	iBit := byte(2)
	sort.SliceStable(tc.cols, func(a, b int) bool { return tc.cols[a].cbData > tc.cols[b].cbData })
	for _, c := range tc.cols {
		c.ibData = ibData
		c.iBit = iBit
		switch c.tag {
		case tagLtpRowID:
			c.ibData, c.iBit = 0, 0
		case tagLtpRowVer:
			c.ibData, c.iBit = 4, 1
		default:
			ibData += uint16(c.cbData)
			iBit++
		}
	}
	cbCeb := uint16(len(tc.cols) / 8)
	if len(tc.cols)%8 > 0 {
		cbCeb++
	}
	tc.info.rgibBM = ibData + cbCeb
	tc.info.rgib4b, tc.info.rgib2b, tc.info.rgib1b = 0, 0, 0
	offset := uint16(0)
	for _, c := range tc.cols {
		offset += uint16(c.cbData)
		switch {
		case c.cbData >= 4:
			tc.info.rgib4b, tc.info.rgib2b, tc.info.rgib1b = offset, offset, offset
		case c.cbData == 2:
			tc.info.rgib2b, tc.info.rgib1b = offset, offset
		case c.cbData == 1:
			tc.info.rgib1b = offset
		}
	}
	sort.SliceStable(tc.cols, func(a, b int) bool { return tc.cols[a].tag < tc.cols[b].tag })
}
