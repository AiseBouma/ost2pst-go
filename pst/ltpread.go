package pst

import (
	"fmt"
	"sort"
)

type hnBlock struct {
	buf       []byte
	rgibAlloc []uint16
	clientSig byte
	userRoot  HID
}

func readHeapOnNode(datablocks [][]byte) []hnBlock {
	if len(datablocks) == 0 {
		panic(fmt.Errorf("empty heap-on-node"))
	}
	blocks := make([]hnBlock, 0, len(datablocks))
	for i, buf := range datablocks {
		ibHnpm := int(le16(buf, 0))
		cAlloc := int(le16(buf, ibHnpm))
		b := hnBlock{buf: buf, rgibAlloc: make([]uint16, cAlloc+1)}
		for j := range b.rgibAlloc {
			b.rgibAlloc[j] = le16(buf, ibHnpm+4+2*j)
		}
		if i == 0 {
			b.clientSig = buf[3]
			b.userRoot = HID(le32(buf, 4))
		}
		blocks = append(blocks, b)
	}
	return blocks
}

func (x *XstFile) hidSize(blocks []hnBlock, hid HID) int {
	ix := int(hid.index(x.is4K()))
	if ix == 0 {
		return 0
	}
	b := &blocks[hid.block(x.is4K())]
	return int(b.rgibAlloc[ix]) - int(b.rgibAlloc[ix-1])
}

func (x *XstFile) heapBytes(blocks []hnBlock, hid HID, count, offset int) []byte {
	b := &blocks[hid.block(x.is4K())]
	start := int(b.rgibAlloc[hid.index(x.is4K())-1]) + offset
	return b.buf[start : start+count]
}

type bthHeader struct {
	btype      byte
	cbKey      int
	cbEnt      int
	bIdxLevels int
	hidRoot    HID
}

func (x *XstFile) readBTHHeader(blocks []hnBlock, hid HID) bthHeader {
	d := x.heapBytes(blocks, hid, 8, 0)
	return bthHeader{btype: d[0], cbKey: int(d[1]), cbEnt: int(d[2]), bIdxLevels: int(d[3]), hidRoot: HID(le32(d, 4))}
}

// readBTHRecords returns the leaf records of a BTH.
func (x *XstFile) readBTHRecords(blocks []hnBlock, h bthHeader, hid HID, level int, out *[][]byte) {
	recSize := h.cbKey + h.cbEnt
	if level == 0 {
		if hid.index(x.is4K()) == 0 || recSize == 0 {
			return
		}
		n := x.hidSize(blocks, hid) / recSize
		d := x.heapBytes(blocks, hid, n*recSize, 0)
		for i := 0; i < n; i++ {
			*out = append(*out, d[i*recSize:(i+1)*recSize])
		}
		return
	}
	interSize := h.cbKey + 4
	n := x.hidSize(blocks, hid) / interSize
	d := x.heapBytes(blocks, hid, n*interSize, 0)
	for i := 0; i < n; i++ {
		next := HID(le32(d, i*interSize+h.cbKey))
		x.readBTHRecords(blocks, h, next, level-1, out)
	}
}

func findSubnode(subnodes []SLEntry, nid uint32) int {
	for i := range subnodes {
		if subnodes[i].Nid == uint64(nid) {
			return i
		}
	}
	return -1
}

func (x *XstFile) bytesForHNID(blocks []hnBlock, subnodes []SLEntry, hnid HNID) []byte {
	switch NidType(hnid & 0x1f) {
	case NidHID:
		hid := HID(hnid)
		if hid.index(x.is4K()) != 0 {
			return x.heapBytes(blocks, hid, x.hidSize(blocks, hid), 0)
		}
		return nil
	case NidLTP:
		ix := findSubnode(subnodes, uint32(hnid))
		if ix < 0 {
			panic(fmt.Errorf("subnode %d not found", uint32(hnid)))
		}
		return x.readFullDatablock(subnodes[ix].BidData)
	}
	panic(fmt.Errorf("data storage style not implemented (hnid %#x)", uint32(hnid)))
}

func (x *XstFile) propertiesFromHeap(blocks []hnBlock, subnodes []SLEntry) []Property {
	h := x.readBTHHeader(blocks, blocks[0].userRoot)
	var recs [][]byte
	x.readBTHRecords(blocks, h, h.hidRoot, h.bIdxLevels, &recs)
	props := make([]Property, 0, len(recs))
	for _, r := range recs {
		id, typ, hnid := PropID(le16(r, 0)), PropType(le16(r, 2)), HNID(le32(r, 4))
		var data []byte
		switch typ {
		case PtypInteger16:
			data = u16bytes(uint16(hnid))
		case PtypInteger32:
			data = u32bytes(uint32(hnid))
		case PtypBoolean:
			data = []byte{byte(hnid)}
		default:
			if !hnid.hasValue() {
				continue
			}
			if hnid.isHID() {
				data = x.bytesForHNID(blocks, subnodes, hnid)
			} else { // value in a subnode
				ix := findSubnode(subnodes, uint32(hnid))
				if ix < 0 {
					panic(fmt.Errorf("property %#x: subnode %d not found", id, uint32(hnid)))
				}
				data = x.readFullDatablock(subnodes[ix].BidData)
			}
		}
		props = append(props, Property{ID: id, Type: typ, HNID: hnid, Data: data})
	}
	return props
}

// readPC reads the Property Context of a node (bidData + bidSub).
func (x *XstFile) readPC(nid uint32, bidData, bidSub uint64) []Property {
	blocks := readHeapOnNode(x.readDatablocks(bidData))
	if blocks[0].clientSig != bTypePC {
		panic(fmt.Errorf("NID entry %d is not a PC", nid))
	}
	return x.propertiesFromHeap(blocks, x.getSLEntries(bidSub))
}

// tcHeap holds a TC heap read from the source file.
type tcHeap struct {
	blocks   []hnBlock
	subnodes []SLEntry
}

func (c *Converter) readTCHeader(nid uint32, bidData, bidSub uint64) (*TableContext, tcHeap) {
	x := c.src
	h := tcHeap{blocks: readHeapOnNode(x.readDatablocks(bidData)), subnodes: x.getSLEntries(bidSub)}
	if h.blocks[0].clientSig != bTypeTC {
		panic(fmt.Errorf("NID entry %d is not a TC", nid))
	}
	tc := c.newTC()
	root := h.blocks[0].userRoot
	tc.info = readTCInfo(x.heapBytes(h.blocks, root, tcInfoSize, 0))
	cols := x.heapBytes(h.blocks, root, int(tc.info.cCols)*tcColDescSize, tcInfoSize)
	tc.cols = make([]*colDesc, tc.info.cCols)
	for i := range tc.cols {
		d := cols[i*tcColDescSize:]
		tc.cols[i] = &colDesc{tag: le32(d, 0), ibData: le16(d, 4), cbData: d[6], iBit: d[7]}
	}
	bh := x.readBTHHeader(h.blocks, tc.info.hidRowIndex)
	var recs [][]byte
	x.readBTHRecords(h.blocks, bh, bh.hidRoot, bh.bIdxLevels, &recs)
	for _, r := range recs {
		tc.rowIndexes = append(tc.rowIndexes, tcRowID{rowID: le32(r, 0), rowIndex: le32(r, 4)})
	}
	return tc, h
}

// readTCWithRowData reads a TC including its row matrix; refreshIndex
// renumbers the row indexes to the order the rows were read in.
func (c *Converter) readTCWithRowData(nid uint32, bidData, bidSub uint64, refreshIndex bool) *TableContext {
	tc, h := c.readTCHeader(nid, bidData, bidSub)
	tc.rows = c.rowData(tc, h)
	if refreshIndex {
		for i := range tc.rowIndexes {
			tc.rowIndexes[i].rowIndex = uint32(i)
		}
	}
	return tc
}

// readTCFromPCs reads a TC and rebuilds its rows from the PCs of the row ids,
// since the OST row data does not always reflect the PCs.
func (c *Converter) readTCFromPCs(nbt NBTEntry) *TableContext {
	tc, _ := c.readTCHeader(nbt.Nid, nbt.BidData, nbt.BidSub)
	for i := range tc.rowIndexes {
		tc.rowIndexes[i].rowIndex = uint32(i)
		tc.rowIndexes[i].rowID = c.mapNID(tc.rowIndexes[i].rowID)
		rid := tc.rowIndexes[i]
		ix, ok := c.src.nbtIndex[rid.rowID]
		if !ok {
			panic(fmt.Errorf("TC (nid=%d) refers to an unknown id %d", nbt.Nid, rid.rowID))
		}
		pcNbt := c.src.NBTs[ix]
		props := c.src.readPC(pcNbt.Nid, pcNbt.BidData, pcNbt.BidSub)
		switch nidType(pcNbt.Nid) {
		case NidNormalFolder:
			props = c.validateFolderPC(props)
		case NidNormalMessage, NidAssocMessage:
			props = c.validateMessagePC(props)
		}
		props = append(props, c.replTagProps()...)
		sortProps(props)
		tc.addRow(rid, props)
	}
	return tc
}

func sortProps(props []Property) {
	sort.SliceStable(props, func(i, j int) bool { return props[i].ID < props[j].ID })
}

type rowBlock struct{ buf []byte }

func (c *Converter) rowData(tc *TableContext, h tcHeap) []*rowData {
	x := c.src
	if !tc.info.hnidRows.hasValue() {
		return nil
	}
	var dbs []rowBlock
	if tc.info.hnidRows.isHID() {
		dbs = append(dbs, rowBlock{buf: x.bytesForHNID(h.blocks, h.subnodes, tc.info.hnidRows)})
	} else {
		ix := findSubnode(h.subnodes, uint32(tc.info.hnidRows))
		if ix < 0 {
			panic(fmt.Errorf("sub node NID %d not found", uint32(tc.info.hnidRows)))
		}
		for _, b := range x.readDatablocks(h.subnodes[ix].BidData) {
			dbs = append(dbs, rowBlock{buf: b})
		}
	}
	rows := make([]*rowData, 0, len(tc.rowIndexes))
	for _, ri := range tc.rowIndexes {
		props := c.columnValues(tc, h, dbs, ri.rowIndex)
		rows = append(rows, newRowData(&tc.info, ri.rowID, props, tc.uid))
	}
	return rows
}

func (c *Converter) columnValues(tc *TableContext, h tcHeap, dbs []rowBlock, rowIndex uint32) []Property {
	x := c.src
	bm := int(tc.info.rgibBM)
	if bm == 0 {
		panic(fmt.Errorf("invalid TC row size"))
	}
	cebSize := (int(tc.info.cCols) + 7) / 8
	rowsPerBlock := (MaxBlockSize - blockTrailerSize) / bm
	if x.Type == FileOST {
		rowsPerBlock = (MaxOSTBlockSize - ostBlockTrailerSize) / bm
	}
	bn := int(rowIndex) / rowsPerBlock
	if bn >= len(dbs) {
		panic(fmt.Errorf("data block number out of bounds"))
	}
	db := dbs[bn]
	rowOffset := int(rowIndex) % rowsPerBlock * bm
	ceb := db.buf[rowOffset+int(tc.info.rgib1b) : rowOffset+int(tc.info.rgib1b)+cebSize]
	var props []Property
	for _, col := range tc.cols {
		if ceb[col.iBit/8]&(0x80>>(col.iBit%8)) == 0 {
			continue
		}
		p, err := c.tableCell(h, db, rowOffset, col)
		if err != nil {
			c.status(fmt.Sprintf("Unable to get the value for a table column: prop id=%#x, prop type=%#x (%v)", col.propID(), col.propType(), err))
			continue
		}
		if p.Type == PtypString && p.dataSize() == 0 {
			continue // SCANPST sometimes flags empty string cells
		}
		props = append(props, p)
	}
	return props
}

func (c *Converter) tableCell(h tcHeap, db rowBlock, rowOffset int, col *colDesc) (p Property, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	p = Property{ID: col.propID(), Type: col.propType()}
	n := cellLength(col)
	if n < 0 {
		return p, fmt.Errorf("unexpected property type %#x length %d", col.propType(), col.cbData)
	}
	if n > 0 {
		p.Data = make([]byte, col.cbData)
		copy(p.Data, db.buf[rowOffset+int(col.ibData):rowOffset+int(col.ibData)+n])
		return p, nil
	}
	p.HNID = HNID(le32(db.buf, rowOffset+int(col.ibData)))
	p.Data = c.src.bytesForHNID(h.blocks, h.subnodes, p.HNID)
	return p, nil
}

// cellLength: -1 invalid, 0 variable (by HNID), otherwise the fixed size.
func cellLength(col *colDesc) int {
	cb := int(col.cbData)
	switch col.propType() {
	case PtypBoolean:
		if cb != 1 {
			return -1
		}
	case PtypInteger16:
		if cb != 2 {
			return -1
		}
	case PtypInteger32:
		if cb != 4 {
			return -1
		}
	case PtypInteger64, PtypFloating64, PtypTime:
		if cb != 8 {
			return -1
		}
	case PtypString, PtypString8, PtypBinary, PtypObject, PtypGuid,
		PtypMultipleInteger32, PtypMultipleString, PtypMultipleBinary:
		if cb == 4 {
			return 0
		}
		return -1
	default:
		return -1
	}
	return cb
}
