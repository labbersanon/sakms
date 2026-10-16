package usenet

// ArchiveMemberNames returns filenames advertised in a ZIP or RAR prefix
// (RAR4 header walk, ZIP local headers, RAR5 best-effort video-name scan).
// Truncated first-segment blobs may yield a partial list.
func ArchiveMemberNames(data []byte) []string {
	return archiveMemberNames(data)
}
