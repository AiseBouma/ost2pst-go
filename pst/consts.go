// Package pst reads Unicode OST/PST files and writes Unicode PST files,
// following [MS-PST]. It is a Go port of github.com/Niv2023/ost2pst.
package pst

// Constants applicable to Unicode files ([MS-PST] 2.2.2.6, 2.2.2.7).
const (
	Magic               uint32 = 0x4e444221
	MagicClient         uint16 = 0x4d53
	MinUnicodeVer       uint16 = 0x17
	OSTVersion          uint16 = 0x24 // OST file with 4k pages
	VerClient           uint16 = 19
	MaxBlockSize               = 8192
	MaxDatablockSize           = 8176 // block - block trailer
	MaxXblockEntries           = 1021 // (blocksize - 8)/8
	MaxSLblockEntries          = 340  // (blocksize - 8)/24
	MaxHeapItemSize            = 3580 // [MS-PST] 2.3.3.3 (PC) and 2.3.4.4.2 (TC)
	MaxOSTBlockSize            = 65536
	BTPageEntryBytes           = 488
	BTPageEntryBytesOST        = 4056 // OST 4k page size
	NBTcbEnt                   = 32
	BBTcbEnt                   = 24
	BTcbEnt                    = 24
	NodePageBlockSize          = 512
	AMapPageEntryBytes         = 496
	AMapAllocationSize         = 496 * 8 * 64
	DListOffset                = 0x4200
	FirstAMapOffset            = 0x4400
	HeaderSize                 = 564
	blockTrailerSize           = 16
	ostBlockTrailerSize        = 24
	tcInfoSize                 = 22
	tcColDescSize              = 8
)

// FileType tells whether a source file is an OST or a PST.
type FileType int

const (
	FileOST FileType = iota
	FilePST
)

func (t FileType) String() string {
	if t == FileOST {
		return "OST"
	}
	return "PST"
}

// Encryption methods (bCryptMethod).
const (
	CryptNone    byte = 0x00
	CryptPermute byte = 0x01
	CryptCyclic  byte = 0x02
)

func cryptName(m byte) string {
	switch m {
	case CryptNone:
		return "NDB_CRYPT_NONE"
	case CryptPermute:
		return "NDB_CRYPT_PERMUTE"
	case CryptCyclic:
		return "NDB_CRYPT_CYCLIC"
	}
	return "UNKNOWN"
}

// NidType is the low 5 bits of a NID.
type NidType uint8

const (
	NidHID                  NidType = 0x00
	NidInternal             NidType = 0x01
	NidNormalFolder         NidType = 0x02
	NidSearchFolder         NidType = 0x03
	NidNormalMessage        NidType = 0x04
	NidAttachment           NidType = 0x05
	NidSearchUpdateQueue    NidType = 0x06
	NidSearchCriteriaObject NidType = 0x07
	NidAssocMessage         NidType = 0x08
	NidContentsTableIndex   NidType = 0x0a
	NidReceiveFolderTable   NidType = 0x0b
	NidOutgoingQueueTable   NidType = 0x0c
	NidHierarchyTable       NidType = 0x0d
	NidContentsTable        NidType = 0x0e
	NidAssocContentsTable   NidType = 0x0f
	NidSearchContentsTable  NidType = 0x10
	NidAttachmentTable      NidType = 0x11
	NidRecipientTable       NidType = 0x12
	NidSearchTableIndex     NidType = 0x13
	NidLTP                  NidType = 0x1f
)

// Page types.
const (
	ptypeBBT   byte = 0x80
	ptypeNBT   byte = 0x81
	ptypeFMap  byte = 0x82
	ptypePMap  byte = 0x83
	ptypeAMap  byte = 0x84
	ptypeFPMap byte = 0x85
	ptypeDL    byte = 0x86
)

// Block / heap client types.
const (
	bTypeD   byte = 0x00
	bTypeX   byte = 0x01
	bTypeS   byte = 0x02
	bTypeTC  byte = 0x7c
	bTypeBTH byte = 0xb5
	bTypePC  byte = 0xbc
)

// Special NIDs.
const (
	NidMessageStore             uint32 = 0x0021
	NidNameToIDMap              uint32 = 0x0061
	NidRootFolder               uint32 = 0x0122
	NidSearchManagementQueue    uint32 = 0x01E1
	NidSearchActivityList       uint32 = 0x0201
	NidHierarchyTableTemplate   uint32 = 0x060D
	NidContentsTableTemplate    uint32 = 0x060E
	NidAssocContentsTableTempl  uint32 = 0x060F
	NidSearchContentsTableTempl uint32 = 0x0610
	NidOutgoingQueueTableNid    uint32 = 0x064C
	NidRecipientTableTemplate   uint32 = 0x0692
	NidReceiveFolderTableNid    uint32 = 0x062B
	NidAttachmentTableTemplate  uint32 = 0x0671
	NidSpamSearchFolder         uint32 = 0x2223
	NidTopPersonalFolder        uint32 = 0x8022
	NidSearchFolderRoot         uint32 = 0x8042
	NidDeletedItems             uint32 = 0x8062 // == NID_DELETED_MESSAGES_FOLDER
)

// Property types.
type PropType uint16

const (
	PtypBinary            PropType = 0x0102
	PtypBoolean           PropType = 0x000b
	PtypFloating64        PropType = 0x0005
	PtypGuid              PropType = 0x0048
	PtypInteger16         PropType = 0x0002
	PtypInteger32         PropType = 0x0003
	PtypInteger64         PropType = 0x0014
	PtypMultipleInteger32 PropType = 0x1003
	PtypObject            PropType = 0x000d
	PtypString            PropType = 0x001f
	PtypString8           PropType = 0x001e
	PtypMultipleString    PropType = 0x101F
	PtypMultipleBinary    PropType = 0x1102
	PtypTime              PropType = 0x0040
)

// Property ids used by the converter.
type PropID uint16

const (
	PidTagRecordKey                             PropID = 0x0FF9
	PidTagIpmSubTreeEntryId                     PropID = 0x35E0
	PidTagIpmWastebasketEntryId                 PropID = 0x35E3
	PidTagFinderEntryId                         PropID = 0x35E7
	PidTagContentCount                          PropID = 0x3602
	PidTagContentUnreadCount                    PropID = 0x3603
	PidTagSubfolders                            PropID = 0x360A
	PidTagSubjectW                              PropID = 0x0037
	PidTagDisplayCcW                            PropID = 0x0E03
	PidTagDisplayToW                            PropID = 0x0E04
	PidTagMessageFlags                          PropID = 0x0E07
	PidTagMessageDeliveryTime                   PropID = 0x0E06
	PidTagSentRepresentingNameW                 PropID = 0x0042
	PidTagClientSubmitTime                      PropID = 0x0039
	PidTagCreationTime                          PropID = 0x3007
	PidTagLastModificationTime                  PropID = 0x3008
	PidTagRecipientType                         PropID = 0x0c15
	PidTagEmailAddress                          PropID = 0x3003
	PidTagAttachFilenameW                       PropID = 0x3704
	PidTagAttachMethod                          PropID = 0x3705
	PidTagAttachDataBinary                      PropID = 0x3701
	PidTagDisplayName                           PropID = 0x3001
	PidTagReplItemid                            PropID = 0x0E30
	PidTagReplChangenum                         PropID = 0x0E33
	PidTagReplVersionHistory                    PropID = 0x0E34
	PidTagReplFlags                             PropID = 0x0E38
	PidTagContainerClass                        PropID = 0x3613
	PidTagPstHiddenCount                        PropID = 0x6635
	PidTagPstHiddenUnread                       PropID = 0x6636
	PidTagLtpRowId                              PropID = 0x67F2
	PidTagLtpRowVer                             PropID = 0x67F3
	PidTagImportance                            PropID = 0x0017
	PidTagMessageClassW                         PropID = 0x001A
	PidTagSensitivity                           PropID = 0x0036
	PidTagMessageToMe                           PropID = 0x0057
	PidTagMessageCcMe                           PropID = 0x0058
	PidTagConversationTopicW                    PropID = 0x0070
	PidTagConversationIndex                     PropID = 0x0071
	PidTagMessageSize                           PropID = 0x0E08
	PidTagMessageStatus                         PropID = 0x0E17
	PidTagReplCopiedfromVersionhistory          PropID = 0x0E3C
	PidTagReplCopiedfromItemid                  PropID = 0x0E3D
	PidTagItemTemporaryFlags                    PropID = 0x1097
	PidTagSecureSubmitFlags                     PropID = 0x65C6
	PidTagMessageClass                          PropID = 0x001A
	PidTagOfflineAddressBookName                PropID = 0x6800
	PidTagSendOutlookRecallReport               PropID = 0x6803
	PidTagOfflineAddressBookTruncatedProperties PropID = 0x6805
	PidTagViewDescriptorFlags                   PropID = 0x7003
	PidTagViewDescriptorLinkTo                  PropID = 0x7004
	PidTagViewDescriptorViewFolder              PropID = 0x7005
	PidTagViewDescriptorName                    PropID = 0x7006
	PidTagViewDescriptorVersion                 PropID = 0x7007
	PidTagParentDisplayW                        PropID = 0x0E05
	PidTagExchangeRemoteHeader                  PropID = 0x0E2A
	PidTagLtpParentNid                          PropID = 0x67F1
	PidTagResponsibility                        PropID = 0x0E0F
	PidTagObjectType                            PropID = 0x0FFE
	PidTagEntryId                               PropID = 0x0FFF
	PidTagAddressType                           PropID = 0x3002
	PidTagSearchKey                             PropID = 0x300B
	PidTagDisplayType                           PropID = 0x3900
	PidTag7BitDisplayName                       PropID = 0x39FF
	PidTagSendRichInfo                          PropID = 0x3A40
	PidTagAttachSize                            PropID = 0x0E20
	PidTagRenderingPosition                     PropID = 0x370B
	PidTagValidFolderMask                       PropID = 0x35DF
	PidTagPstPassword                           PropID = 0x67FF
	pidTag67F4                                  PropID = 0x67F4
)
