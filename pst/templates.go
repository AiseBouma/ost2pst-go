package pst

import "fmt"

// Builds the objects required by [MS-PST] 2.7 (minimum PST requirements).

type templates struct {
	hierarchy, contents, assocContents, searchContents, recipient, attachment               *TableContext
	hierarchyBid, contentsBid, assocContentsBid, searchContentsBid, recipientBid, attachBid uint64
}

var pstUID = []byte{0x45, 0x7C, 0x5C, 0x24, 0x7F, 0x65, 0xF5, 0x45, 0x87, 0xF8, 0xD6, 0xA3, 0x0D, 0x30, 0xC2, 0x69}

// NID 0xC01 is undocumented but required by Outlook/SCANPST (copied from an empty PST).
var nid3073data = []byte{
	0x18, 0x00, 0xEC, 0x9C, 0x20, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x40, 0x00, 0x00, 0x00,
	0xB5, 0x10, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x0C, 0x00, 0x10, 0x00,
	0x18, 0x00,
}

func (c *Converter) buildPSTObjects(exportFolder *Folder, displayName string) {
	c.pstMessageStore(displayName)
	if _, ok := c.src.nbtIndex[NidNameToIDMap]; !ok {
		panic(fmt.Errorf("missing mandatory Name-to-ID NID (0x61)"))
	}
	c.pstTemplateObjects()
	c.createPSTFolderStructure(exportFolder)
}

func entryID(nid uint32) []byte {
	b := make([]byte, 0, 24)
	b = append(b, 0, 0, 0, 0)
	b = append(b, pstUID...)
	return append(b, u32bytes(nid)...)
}

func (c *Converter) pstMessageStore(displayName string) {
	props := []Property{
		propBinary(PidTagRecordKey, PtypBinary, pstUID),
		propString(PidTagDisplayName, displayName),
		propBinary(PidTagIpmSubTreeEntryId, PtypBinary, entryID(NidTopPersonalFolder)),
		propBinary(PidTagIpmWastebasketEntryId, PtypBinary, entryID(NidDeletedItems)),
		propBinary(PidTagFinderEntryId, PtypBinary, entryID(NidSearchFolderRoot)),
		propBinary(PidTagReplVersionHistory, PtypBinary, replVersionHistory),
		propU32(PidTagReplFlags, 0),
		propU32(PidTagValidFolderMask, 0x0089),
		propU32(PidTagPstPassword, 0),
	}
	sortProps(props)
	c.addNDBData(NidMessageStore, newPCBTH(c.w, props).heapBlocks(bTypePC), 0, 0)
}

func (c *Converter) pstTemplateObjects() {
	t := &c.tpl
	t.hierarchy = c.hierarchyTemplate()
	t.hierarchyBid = c.addNIDtc(NidHierarchyTableTemplate, t.hierarchy)
	t.contents = c.contentsTemplate()
	t.contentsBid = c.addNIDtc(NidContentsTableTemplate, t.contents)
	t.assocContents = c.assocContentsTemplate()
	t.assocContentsBid = c.addNIDtc(NidAssocContentsTableTempl, t.assocContents)
	t.searchContents = c.searchContentsTemplate()
	t.searchContentsBid = c.addNIDtc(NidSearchContentsTableTempl, t.searchContents)
	t.recipient = c.recipientTemplate()
	t.recipientBid = c.addNIDtc(NidRecipientTableTemplate, t.recipient)
	t.attachment = c.attachmentTemplate()
	t.attachBid = c.addNIDtc(NidAttachmentTableTemplate, t.attachment)

	// search activity list
	r := c.w.addDatablock(u32bytes(NidSpamSearchFolder), 0)
	c.out.addNBT(NBTEntry{Nid: NidSearchActivityList, BidData: r.Bid})

	// receive folder table (not in MS-PST; replicates an empty PST)
	tc := c.newTC()
	tc.addColumn(PidTagLtpRowId, PtypInteger32)
	tc.addColumn(PidTagLtpRowVer, PtypInteger32)
	tc.addColumn(PidTagMessageClassW, PtypString)
	tc.addColumn(0x6605, PtypInteger32)
	tc.addRowNID(1, []Property{
		emptyProp(PidTagMessageClassW, PtypString, c.defaultTime),
		propU32(0x6605, NidRootFolder),
	})
	c.addNIDtc(NidReceiveFolderTableNid, tc)

	// outgoing queue table
	tc = c.newTC()
	tc.addColumn(PidTagClientSubmitTime, PtypTime)
	tc.addColumn(0x0E10, PtypInteger32)
	tc.addColumn(0x0E14, PtypInteger32)
	tc.addColumn(PidTagLtpRowId, PtypInteger32)
	tc.addColumn(PidTagLtpRowVer, PtypInteger32)
	c.addNIDtc(NidOutgoingQueueTableNid, tc)

	// undocumented objects
	tc = c.newTC()
	tc.addColumn(PidTagMessageClassW, PtypString)
	tc.addColumn(0x0E30, PtypBinary)
	tc.addColumn(0x0E31, PtypBinary)
	tc.addColumn(0x0E33, PtypInteger64)
	tc.addColumn(0x0E34, PtypBinary)
	tc.addColumn(0x0E38, PtypInteger32)
	tc.addColumn(0x0E3E, PtypBinary)
	tc.addColumn(PidTagLtpRowId, PtypInteger32)
	tc.addColumn(PidTagLtpRowVer, PtypInteger32)
	c.addNIDtc(0x06D7, tc)

	tc = c.newTC()
	tc.addColumn(0x0E33, PtypInteger64)
	tc.addColumn(0x3007, PtypTime)
	tc.addColumn(PidTagLtpRowId, PtypInteger32)
	tc.addColumn(PidTagLtpRowVer, PtypInteger32)
	c.addNIDtc(0x06F8, tc)

	c.out.addNBT(NBTEntry{Nid: 0x01E1})
	c.out.addNBT(NBTEntry{Nid: 0x0EC1})
	r = c.w.addDatablock(append([]byte(nil), nid3073data...), 0)
	c.out.addNBT(NBTEntry{Nid: 0x0C01, BidData: r.Bid})
}

func (c *Converter) createPSTFolderStructure(exportFolder *Folder) {
	c.folderPC(NidRootFolder, "", NidRootFolder, true)
	top := c.folderPC(NidTopPersonalFolder, "Top of Personal Folders", NidRootFolder, true)
	searchRoot := c.folderPC(NidSearchFolderRoot, "Search Root", NidRootFolder, false)
	spam := c.folderPC(NidSpamSearchFolder, "SPAM Search Folder 2", NidRootFolder, false)
	deleted := c.folderPC(NidDeletedItems, "Deleted Items", NidTopPersonalFolder, false)

	c.folderObjects(NidRootFolder, []*Folder{top, searchRoot, spam})
	c.folderObjects(NidTopPersonalFolder, []*Folder{deleted, exportFolder})
	c.folderObjects(NidSearchFolderRoot, nil)
	c.searchFolderObjects(NidSpamSearchFolder)
	c.folderObjects(NidDeletedItems, nil)
}

func folderPCProps(name string, count, unread uint32, subfolders bool) []Property {
	return []Property{
		propString(PidTagDisplayName, name),
		propU32(PidTagContentCount, count),
		propU32(PidTagContentUnreadCount, unread),
		propBool(PidTagSubfolders, subfolders),
	}
}

func (c *Converter) folderPC(nid uint32, name string, parent uint32, hasSubfolders bool) *Folder {
	props := folderPCProps(name, 0, 0, hasSubfolders)
	c.addNDBData(nid, newPCBTH(c.w, props).heapBlocks(bTypePC), 0, parent)
	return &Folder{Nid: nid, Properties: props}
}

func (c *Converter) folderObjects(nid uint32, subfolders []*Folder) {
	ht := changeNidType(nid, NidHierarchyTable)
	ct := changeNidType(nid, NidContentsTable)
	at := changeNidType(nid, NidAssocContentsTable)
	if len(subfolders) > 0 {
		c.addNIDtc(ht, c.folderHierarchyTC(subfolders))
	} else {
		c.addNIDReference(ht, c.tpl.hierarchyBid)
	}
	c.addNIDReference(ct, c.tpl.contentsBid)
	c.addNIDReference(at, c.tpl.assocContentsBid)
}

func (c *Converter) searchFolderObjects(nid uint32) {
	c.out.addNBT(NBTEntry{Nid: changeNidType(nid, NidSearchUpdateQueue)})
	props := []Property{propU32(0x660B, 0)}
	c.addNDBData(changeNidType(nid, NidSearchCriteriaObject), newPCBTH(c.w, props).heapBlocks(bTypePC), 0, 0)
	c.addNIDReference(changeNidType(nid, NidSearchContentsTable), c.tpl.searchContentsBid)
}

func (c *Converter) folderHierarchyTC(subfolders []*Folder) *TableContext {
	tc := c.tpl.hierarchy.clone()
	tc.resetRows()
	sorted := append([]*Folder(nil), subfolders...)
	for i := 1; i < len(sorted); i++ { // stable insertion sort by nid
		for j := i; j > 0 && sorted[j].Nid < sorted[j-1].Nid; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	for _, sf := range sorted {
		props := append([]Property(nil), sf.Properties...)
		if sf.Nid != NidSpamSearchFolder { // SPAM Search Folder 2 has no repl items
			props = append(props, c.replTagProps()...)
		}
		tc.addRowNID(sf.Nid, props)
	}
	return tc
}

func (c *Converter) addNIDtc(nid uint32, tc *TableContext) uint64 {
	return c.addNDBData(nid, newTCBTH(c.w, tc, false).heapBlocks(bTypeTC), 0, 0)
}

func (c *Converter) addNIDReference(nid uint32, bid uint64) {
	c.out.addNBT(NBTEntry{Nid: nid, BidData: bid})
	c.out.incrementRefCount(bid)
}

// addNDBData writes heap blocks and adds a NBT entry for them.
func (c *Converter) addNDBData(nid uint32, blocks [][]byte, bidSub uint64, nidParent uint32) uint64 {
	var r BREF
	if len(blocks) == 1 {
		r = c.w.addDatablock(blocks[0], 0)
	} else {
		r = c.w.addDataTree(blocks)
	}
	c.out.addNBT(NBTEntry{Nid: nid, BidData: r.Bid, BidSub: bidSub, NidParent: nidParent})
	return r.Bid
}

type col struct {
	id  PropID
	typ PropType
}

func (c *Converter) templateTC(cols []col) *TableContext {
	tc := c.newTC()
	for _, k := range cols {
		tc.addColumn(k.id, k.typ)
	}
	return tc
}

// [MS-PST] 2.4.4.4.1
func (c *Converter) hierarchyTemplate() *TableContext {
	return c.templateTC([]col{
		{PidTagLtpRowId, PtypInteger32}, {PidTagLtpRowVer, PtypInteger32},
		{PidTagReplItemid, PtypBinary}, // MS-PST says int32, Outlook uses binary
		{PidTagReplChangenum, PtypInteger64}, {PidTagReplVersionHistory, PtypBinary},
		{PidTagReplFlags, PtypInteger32}, {PidTagDisplayName, PtypString},
		{PidTagContentCount, PtypInteger32}, {PidTagContentUnreadCount, PtypInteger32},
		{PidTagSubfolders, PtypBoolean},
		{PidTagContainerClass, PtypString}, // MS-PST says binary, Outlook uses string
		{PidTagPstHiddenCount, PtypInteger32}, {PidTagPstHiddenUnread, PtypInteger32},
	})
}

// [MS-PST] 2.4.4.5.1
func (c *Converter) contentsTemplate() *TableContext {
	return c.templateTC([]col{
		{PidTagLtpRowId, PtypInteger32}, {PidTagLtpRowVer, PtypInteger32},
		{PidTagImportance, PtypInteger32}, {PidTagMessageClassW, PtypString},
		{PidTagSensitivity, PtypInteger32}, {PidTagSubjectW, PtypString},
		{PidTagClientSubmitTime, PtypTime}, {PidTagSentRepresentingNameW, PtypString},
		{PidTagMessageToMe, PtypBoolean}, {PidTagMessageCcMe, PtypBoolean},
		{PidTagConversationTopicW, PtypString}, {PidTagConversationIndex, PtypBinary},
		{PidTagDisplayCcW, PtypString}, {PidTagDisplayToW, PtypString},
		{PidTagMessageDeliveryTime, PtypTime}, {PidTagMessageFlags, PtypInteger32},
		{PidTagMessageSize, PtypInteger32}, {PidTagMessageStatus, PtypInteger32},
		{PidTagReplItemid, PtypBinary}, {PidTagReplChangenum, PtypInteger64},
		{PidTagReplVersionHistory, PtypBinary}, {PidTagReplFlags, PtypInteger32},
		{PidTagReplCopiedfromVersionhistory, PtypBinary}, {PidTagReplCopiedfromItemid, PtypBinary},
		{PidTagItemTemporaryFlags, PtypInteger32}, {PidTagLastModificationTime, PtypTime},
		{PidTagSecureSubmitFlags, PtypInteger32},
		{0x3013, PtypBinary}, // flagged as missing by SCANPST
	})
}

// [MS-PST] 2.4.4.6.1
func (c *Converter) assocContentsTemplate() *TableContext {
	return c.templateTC([]col{
		{PidTagMessageClass, PtypString}, {PidTagMessageFlags, PtypInteger32},
		{PidTagMessageStatus, PtypInteger32}, {PidTagDisplayName, PtypString},
		{PidTagLtpRowId, PtypInteger32}, {PidTagLtpRowVer, PtypInteger32},
		{PidTagOfflineAddressBookName, PtypString}, {PidTagSendOutlookRecallReport, PtypBoolean},
		{PidTagOfflineAddressBookTruncatedProperties, PtypMultipleInteger32},
		{PidTagViewDescriptorFlags, PtypInteger32}, {PidTagViewDescriptorLinkTo, PtypBinary},
		{PidTagViewDescriptorViewFolder, PtypBinary}, {PidTagViewDescriptorName, PtypString},
		{PidTagViewDescriptorVersion, PtypInteger32},
		{0x682f, PtypString}, // flagged as missing by SCANPST
	})
}

// [MS-PST] 2.4.8.6.2.1
func (c *Converter) searchContentsTemplate() *TableContext {
	return c.templateTC([]col{
		{PidTagLtpRowId, PtypInteger32}, {PidTagLtpRowVer, PtypInteger32},
		{PidTagImportance, PtypInteger32}, {PidTagMessageClassW, PtypString},
		{PidTagSensitivity, PtypInteger32}, {PidTagMessageFlags, PtypInteger32},
		{PidTagMessageStatus, PtypInteger32}, {PidTagSubjectW, PtypString},
		{PidTagSentRepresentingNameW, PtypString}, {PidTagMessageToMe, PtypBoolean},
		{PidTagMessageCcMe, PtypBoolean}, {PidTagDisplayCcW, PtypString},
		{PidTagDisplayToW, PtypString}, {PidTagParentDisplayW, PtypString},
		{PidTagMessageDeliveryTime, PtypTime}, {PidTagMessageSize, PtypInteger32},
		{PidTagExchangeRemoteHeader, PtypBoolean}, {PidTagLastModificationTime, PtypTime},
		{PidTagLtpParentNid, PtypInteger32},
	})
}

// [MS-PST] 2.4.5.3.1
func (c *Converter) recipientTemplate() *TableContext {
	return c.templateTC([]col{
		{PidTagLtpRowId, PtypInteger32}, {PidTagLtpRowVer, PtypInteger32},
		{PidTagRecipientType, PtypInteger32}, {PidTagResponsibility, PtypBoolean},
		{PidTagRecordKey, PtypBinary}, {PidTagObjectType, PtypInteger32},
		{PidTagEntryId, PtypBinary}, {PidTagDisplayName, PtypString},
		{PidTagAddressType, PtypString}, {PidTagEmailAddress, PtypString},
		{PidTagSearchKey, PtypBinary}, {PidTagDisplayType, PtypInteger32},
		{PidTag7BitDisplayName, PtypString}, {PidTagSendRichInfo, PtypBoolean},
	})
}

// [MS-PST] 2.4.6.1.1
func (c *Converter) attachmentTemplate() *TableContext {
	return c.templateTC([]col{
		{PidTagAttachSize, PtypInteger32}, {PidTagAttachFilenameW, PtypString},
		{PidTagAttachMethod, PtypInteger32}, {PidTagRenderingPosition, PtypInteger32},
		{PidTagLtpRowId, PtypInteger32}, {PidTagLtpRowVer, PtypInteger32},
	})
}

// --- validation of source objects ------------------------------------------

func (c *Converter) requireProp(props []Property, id PropID, typ PropType) []Property {
	if findPropTyped(props, id, typ) < 0 {
		props = append(props, emptyProp(id, typ, c.defaultTime))
	}
	return props
}

func removeProp(props []Property, id PropID) []Property {
	if ix := findProp(props, id); ix >= 0 {
		props = append(props[:ix:ix], props[ix+1:]...)
	}
	return props
}

// [MS-PST] 2.4.4.1.1
func (c *Converter) validateFolderPC(p []Property) []Property {
	p = c.requireProp(p, PidTagDisplayName, PtypString)
	p = c.requireProp(p, PidTagContentCount, PtypInteger32)
	p = c.requireProp(p, PidTagContentUnreadCount, PtypInteger32)
	p = c.requireProp(p, PidTagSubfolders, PtypBoolean)
	return removeProp(p, pidTag67F4)
}

// [MS-PST] 2.4.5.1.1
func (c *Converter) validateMessagePC(p []Property) []Property {
	p = c.requireProp(p, PidTagMessageClass, PtypString)
	p = c.requireProp(p, PidTagMessageFlags, PtypInteger32)
	p = c.requireProp(p, PidTagMessageSize, PtypInteger32)
	p = c.requireProp(p, PidTagMessageStatus, PtypInteger32)
	p = c.requireProp(p, PidTagCreationTime, PtypTime)
	p = c.requireProp(p, PidTagLastModificationTime, PtypTime)
	p = c.requireProp(p, PidTagSearchKey, PtypBinary)
	return removeProp(p, pidTag67F4)
}

func requireCols(tc *TableContext, cols []col) {
	for _, k := range cols {
		if tc.findColumn(k.id, k.typ) < 0 {
			tc.addColumn(k.id, k.typ)
		}
	}
	tc.removeColumn(pidTag67F4, PtypInteger64)
}

var contentsRequiredCols = []col{
	{PidTagImportance, PtypInteger32}, {PidTagMessageClassW, PtypString},
	{PidTagSensitivity, PtypInteger32}, {PidTagSubjectW, PtypString},
	{PidTagClientSubmitTime, PtypTime}, {PidTagSentRepresentingNameW, PtypString},
	{PidTagMessageToMe, PtypBoolean}, {PidTagMessageCcMe, PtypBoolean},
	{PidTagConversationTopicW, PtypString}, {PidTagConversationIndex, PtypBinary},
	{PidTagDisplayCcW, PtypString}, {PidTagDisplayToW, PtypString},
	{PidTagMessageDeliveryTime, PtypTime}, {PidTagMessageFlags, PtypInteger32},
	{PidTagMessageSize, PtypInteger32}, {PidTagMessageStatus, PtypInteger32},
	{PidTagReplItemid, PtypBinary}, {PidTagReplChangenum, PtypInteger64},
	{PidTagReplVersionHistory, PtypBinary}, {PidTagReplFlags, PtypInteger32},
	{PidTagReplCopiedfromVersionhistory, PtypBinary}, {PidTagReplCopiedfromItemid, PtypBinary},
	{PidTagItemTemporaryFlags, PtypInteger32}, {PidTagLastModificationTime, PtypTime},
	{PidTagSecureSubmitFlags, PtypInteger32}, {PidTagLtpRowId, PtypInteger32},
	{PidTagLtpRowVer, PtypInteger32},
}

var hierarchyRequiredCols = []col{
	{PidTagReplItemid, PtypBinary}, {PidTagReplChangenum, PtypInteger64},
	{PidTagReplVersionHistory, PtypBinary}, {PidTagReplFlags, PtypInteger32},
	{PidTagDisplayName, PtypString}, {PidTagContentCount, PtypInteger32},
	{PidTagContentUnreadCount, PtypInteger32}, {PidTagSubfolders, PtypBoolean},
	{PidTagContainerClass, PtypBinary}, {PidTagPstHiddenCount, PtypInteger32},
	{PidTagPstHiddenUnread, PtypInteger32}, {PidTagLtpRowId, PtypInteger32},
	{PidTagLtpRowVer, PtypInteger32},
}
