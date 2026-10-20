package usenet

// ArchiveMemberNames returns filenames advertised in a ZIP or RAR prefix
// (RAR4 header walk, ZIP local headers, RAR5 best-effort video-name scan).
// Truncated first-segment blobs may yield a partial list.
func ArchiveMemberNames(data []byte) []string {
	return archiveMemberNames(data)
}

// ArchiveFileCRCs returns unpacked inner-file CRC32 values from a ZIP or RAR4
// first-segment prefix. Empty when the blob is not those formats or the CRC
// field is zero (data descriptor / not yet written).
func ArchiveFileCRCs(data []byte) []ArchiveFileCRC {
	return archiveFileCRCs(data)
}
