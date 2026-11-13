package makemkv

// Attr is the number MakeMKV uses to say which attribute a CINFO, TINFO or
// SINFO line carries. The numbers come from the AP_ItemAttributeId enum in
// apdefs.h in the open-source makemkv-oss package.
//
// Only the attributes Trayarr reads are named here. Every attribute is still
// kept in the Attrs maps, so nothing is lost if MakeMKV adds new ones.
// Check these against apdefs.h whenever the pinned MakeMKV version changes.
type Attr int

const (
	AttrType          Attr = 1  // stream type: "Video", "Audio", "Subtitles"
	AttrName          Attr = 2  // disc or title name
	AttrLangCode      Attr = 3  // ISO 639-2 language code, e.g. "eng"
	AttrLangName      Attr = 4  // language name, e.g. "English"
	AttrCodecID       Attr = 5  // e.g. "V_MPEGH/ISO/HEVC"
	AttrCodecShort    Attr = 6  // e.g. "HEVC"
	AttrCodecLong     Attr = 7  // e.g. "MpegH HEVC Main10@L5.1"
	AttrChapterCount  Attr = 8  // number of chapters in a title
	AttrDuration      Attr = 9  // title length, "h:mm:ss"
	AttrDiskSize      Attr = 10 // human-readable size, e.g. "31.4 GB"
	AttrDiskSizeBytes Attr = 11 // exact size in bytes
	AttrSourceFile    Attr = 16 // the playlist or clip a title comes from, e.g. "00800.mpls"
	AttrVideoSize     Attr = 19 // e.g. "3840x2160"
	AttrSegmentsCount Attr = 25 // how many clips the title plays
	AttrSegmentsMap   Attr = 26 // which clips, in order, e.g. "3,13,15,1"
	AttrOutputFile    Attr = 27 // the file name MakeMKV would write, e.g. "Movie_t00.mkv"
	AttrVolumeName    Attr = 32 // the disc's volume label, e.g. "THE_WHOLE_BLOODY_AFFAIR_UHD_D1"
	AttrComment       Attr = 49 // free-text comment MakeMKV attaches to a title
)
