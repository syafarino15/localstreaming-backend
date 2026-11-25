// Package media implements the ingest engine that maps the shared storage
// directory onto catalog rows in PostgreSQL.
//
// Operational flow: crew copies video files onto a USB flashdisk, plugs it into
// the node, and the shared folder appears under MEDIA_ROOT/Movies. This package
// walks that directory, derives metadata from filenames or an optional
// catalog.json, computes a checksum, and writes a row whose file_path becomes
// the stream URL exposed to the TV.
package media

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// videoExtensions is the set of containers the TV client can play through the
// byte-range endpoint or through HLS passthrough.
var videoExtensions = map[string]bool{
	".mp4":  true,
	".mkv":  true,
	".mov":  true,
	".m4v":  true,
	".webm": true,
	".avi":  true,
	".m3u8": true,
	".ts":   true,
}

// subtitleExtensions lists the sidecar subtitle formats the player can render.
var subtitleExtensions = map[string]bool{
	".srt": true,
	".vtt": true,
	".ass": true,
	".ssa": true,
}

// posterExtensions lists accepted artwork formats.
var posterExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
}

// qualityTokens are stripped from filenames because they describe the encode,
// not the title.
var qualityTokens = []string{
	"480p", "540p", "576p", "720p", "1080p", "1080i", "1440p", "2160p", "4k", "8k", "uhd",
	"web-dl", "webdl", "webrip", "web", "bluray", "blu-ray", "bdrip", "brrip", "dvdrip",
	"hdrip", "hdtv", "camrip", "hdcam", "remux", "proper", "repack", "extended",
	"x264", "x265", "h264", "h265", "hevc", "avc", "xvid", "divx", "10bit", "8bit",
	"aac", "ac3", "eac3", "ddp", "dd5", "dts", "truehd", "atmos", "flac",
	"hdr", "hdr10", "dv", "dolby", "vision",
	"dual", "subbed", "dubbed", "sub", "ind", "eng", "indo",
	"nf", "amzn", "dsnp", "hmax", "webrip2", "complete",
}

// yearPattern matches a release year as a standalone token.
var yearPattern = regexp.MustCompile(`(?:^|[\s._\-(\[])((?:19|20)\d{2})(?:[\s._\-)\]]|$)`)

// multiSpace collapses repeated whitespace.
var multiSpace = regexp.MustCompile(`\s+`)

// slugUnsafe matches every character that cannot appear in a slug.
var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// IsVideo reports whether the path has a supported video extension.
func IsVideo(path string) bool {
	return videoExtensions[strings.ToLower(ext(path))]
}

// IsSubtitle reports whether the path has a supported subtitle extension.
func IsSubtitle(path string) bool {
	return subtitleExtensions[strings.ToLower(ext(path))]
}

// IsPoster reports whether the path has a supported artwork extension.
func IsPoster(path string) bool {
	return posterExtensions[strings.ToLower(ext(path))]
}

// Container returns the normalised container without the leading dot.
func Container(path string) string {
	return strings.TrimPrefix(strings.ToLower(ext(path)), ".")
}

// ParsedName is the metadata recovered from a filename.
type ParsedName struct {
	Title string
	Year  int
}

// ParseFilename derives a human title and release year from a video filename.
//
// Examples:
//
//	"Laskar_Pelangi.2008.1080p.WEB-DL.mkv"      -> "Laskar Pelangi", 2008
//	"inception (2010) [BluRay] 720p.mp4"        -> "Inception", 2010
//	"Film Indonesia Terbaru.mkv"                -> "Film Indonesia Terbaru", 0
func ParseFilename(filename string) ParsedName {
	name := strings.TrimSuffix(filename, ext(filename))

	// Normalise separators so token matching is uniform.
	name = strings.NewReplacer("_", " ", ".", " ", "[", " ", "]", " ", "{", " ", "}", " ", "(", " ", ")", " ").Replace(name)

	var year int
	if match := yearPattern.FindStringSubmatch(" " + name + " "); len(match) == 2 {
		if parsed, err := strconv.Atoi(match[1]); err == nil && parsed >= 1900 && parsed <= 2100 {
			year = parsed
			// Remove the year token so it never appears in the title.
			name = strings.Replace(name, match[1], " ", 1)
		}
	}

	fields := strings.Fields(name)
	kept := make([]string, 0, len(fields))
	for _, field := range fields {
		if isQualityToken(field) {
			continue
		}
		kept = append(kept, field)
	}

	title := multiSpace.ReplaceAllString(strings.Join(kept, " "), " ")
	title = strings.Trim(title, " -")
	if title == "" {
		// Fall back to the original basename so a file always has a title.
		title = strings.TrimSuffix(filename, ext(filename))
		title = multiSpace.ReplaceAllString(strings.NewReplacer("_", " ", ".", " ").Replace(title), " ")
		title = strings.TrimSpace(title)
	}
	if title == "" {
		// Degenerate names such as ".mkv" or "1080p.mp4" carry no title at all.
		title = "Tanpa Judul"
	}

	return ParsedName{Title: TitleCase(title), Year: year}
}

// Slug builds a URL safe identifier from a title and year.
func Slug(title string, year int) string {
	base := strings.ToLower(title)
	base = slugUnsafe.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "movie"
	}
	if year > 0 {
		return base + "-" + strconv.Itoa(year)
	}
	return base
}

// TitleCase upper cases the first letter of every word while preserving the
// rest of the casing, which keeps acronyms such as "3D" or "II" intact.
func TitleCase(s string) string {
	runes := []rune(s)
	startOfWord := true
	for i, r := range runes {
		if startOfWord && unicode.IsLetter(r) {
			runes[i] = unicode.ToUpper(r)
			startOfWord = false
			continue
		}
		if unicode.IsSpace(r) {
			startOfWord = true
			continue
		}
		startOfWord = false
	}
	return string(runes)
}

// LanguageFromFilename guesses a subtitle language from tokens such as
// ".id.srt", ".en.srt" or ".indonesia.srt".
func LanguageFromFilename(filename string) (code, label string) {
	lower := strings.ToLower(strings.TrimSuffix(filename, ext(filename)))
	tokens := strings.FieldsFunc(lower, func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == ' '
	})
	if len(tokens) == 0 {
		return "und", "Default"
	}

	last := tokens[len(tokens)-1]
	switch last {
	case "id", "ind", "indo", "indonesia", "idn":
		return "id", "Indonesia"
	case "en", "eng", "english", "inggris":
		return "en", "English"
	case "ms", "may", "melayu":
		return "ms", "Melayu"
	case "zh", "chi", "chinese", "mandarin":
		return "zh", "Mandarin"
	case "ar", "ara", "arab":
		return "ar", "Arab"
	case "ja", "jpn", "japanese":
		return "ja", "Japanese"
	case "ko", "kor", "korean":
		return "ko", "Korean"
	}
	return "und", "Default"
}

func isQualityToken(field string) bool {
	normalised := strings.ToLower(strings.Trim(field, " -"))
	if normalised == "" {
		return false
	}
	for _, token := range qualityTokens {
		if normalised == token {
			return true
		}
	}
	// Release group markers such as "-RARBG" or "@Group".
	if strings.HasPrefix(normalised, "@") {
		return true
	}
	return false
}

func ext(path string) string {
	for i := len(path) - 1; i >= 0 && path[i] != '/' && path[i] != '\\'; i-- {
		if path[i] == '.' {
			return path[i:]
		}
	}
	return ""
}
