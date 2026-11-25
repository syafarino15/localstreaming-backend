package media

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

// Metadata is the catalog information resolved for one video file.
type Metadata struct {
	Title            string
	Year             int
	Genres           []string
	Synopsis         string
	Tagline          string
	Director         string
	Cast             []string
	Country          string
	OriginalLanguage string
	AgeRating        string
	Quality          string
	DurationSeconds  int
	Source           string
	Raw              json.RawMessage
	// Sidecars declared explicitly inside catalog.json.
	Poster    string
	Banner    string
	Subtitles []domain.SubtitleTrack
}

// Scanned is one playable file discovered on disk.
type Scanned struct {
	RelPath   string
	AbsPath   string
	Size      int64
	ModTime   int64
	Container string
	PosterRel string
	BannerRel string
	Subtitles []domain.SubtitleTrack
	Metadata  Metadata
}

// catalogFile is the optional manifest a crew can place next to the videos.
type catalogFile struct {
	Movies []catalogEntry `json:"movies"`
}

// catalogEntry describes one movie inside catalog.json. The same shape is
// accepted for a per-file sidecar JSON.
type catalogEntry struct {
	File             string                 `json:"file"`
	Title            string                 `json:"title"`
	Year             int                    `json:"year"`
	Genres           []string               `json:"genres"`
	Synopsis         string                 `json:"synopsis"`
	Tagline          string                 `json:"tagline"`
	Director         string                 `json:"director"`
	Cast             []string               `json:"cast"`
	Country          string                 `json:"country"`
	OriginalLanguage string                 `json:"original_language"`
	AgeRating        string                 `json:"age_rating"`
	Quality          string                 `json:"quality"`
	DurationSeconds  int                    `json:"duration_seconds"`
	Poster           string                 `json:"poster"`
	Banner           string                 `json:"banner"`
	Subtitles        []domain.SubtitleTrack `json:"subtitles"`
	Source           string                 `json:"source"`
	Raw              json.RawMessage        `json:"raw"`
}

// Scan walks the movie library and returns every playable file.
//
// The root may be the shared folder that a flashdisk was copied into. Hidden
// directories and operating system metadata are skipped so a FAT32 disk copied
// from Windows or macOS does not pollute the catalog.
func Scan(root string, maxDepth int) ([]Scanned, error) {
	if maxDepth < 1 {
		maxDepth = 6
	}
	root = filepath.Clean(root)

	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("media: open root %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("media: root %s is not a directory", root)
	}

	manifests := map[string]*catalogFile{}
	perFile := map[string]catalogEntry{}
	videos := make([]string, 0, 128)

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A permission error on one file must not abort the whole scan.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		if depth(rel) > maxDepth {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		name := entry.Name()
		if entry.IsDir() {
			if skipDir(name) {
				return fs.SkipDir
			}
			return nil
		}
		if skipFile(name) {
			return nil
		}

		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}

		lower := strings.ToLower(name)
		switch {
		case lower == "catalog.json":
			if parsed, parseErr := readCatalog(path); parseErr == nil && parsed != nil {
				manifests[filepath.Dir(path)] = parsed
			}
		case strings.HasSuffix(lower, ".json") && !IsVideo(path):
			if parsed, parseErr := readCatalogEntry(path); parseErr == nil && parsed != nil {
				perFile[path] = *parsed
			}
		case IsVideo(path):
			if strings.HasSuffix(lower, ".ts") {
				// Transport stream segments belong to an HLS playlist.
				return nil
			}
			videos = append(videos, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("media: walk %s: %w", root, err)
	}

	sort.Strings(videos)

	results := make([]Scanned, 0, len(videos))
	seenHLSPlaylists := map[string]bool{}

	for _, abs := range videos {
		lower := strings.ToLower(abs)
		// Only keep one playlist per directory to avoid duplicate HLS entries.
		if strings.HasSuffix(lower, ".m3u8") {
			dir := filepath.Dir(abs)
			if seenHLSPlaylists[dir] {
				continue
			}
			seenHLSPlaylists[dir] = true
		}

		stat, statErr := os.Stat(abs)
		if statErr != nil {
			continue
		}

		relToRoot, relErr := filepath.Rel(root, abs)
		if relErr != nil {
			continue
		}

		entry := resolveMetadata(abs, root, manifests, perFile)

		item := Scanned{
			RelPath:   filepath.ToSlash(relToRoot),
			AbsPath:   abs,
			Size:      stat.Size(),
			ModTime:   stat.ModTime().Unix(),
			Container: Container(abs),
			Metadata:  entry,
		}

		posterRel, bannerRel, subtitles := findSidecars(abs, root)

		// A catalog.json declaration always wins over filesystem guessing.
		if entry.Poster != "" {
			if resolved := normaliseSidecar(abs, root, entry.Poster); resolved != "" {
				posterRel = resolved
			}
		}
		if entry.Banner != "" {
			if resolved := normaliseSidecar(abs, root, entry.Banner); resolved != "" {
				bannerRel = resolved
			}
		}
		if len(entry.Subtitles) > 0 {
			if resolved := normaliseSubtitles(abs, root, entry.Subtitles); len(resolved) > 0 {
				subtitles = resolved
			}
		}

		item.PosterRel = posterRel
		item.BannerRel = bannerRel
		if subtitles == nil {
			subtitles = []domain.SubtitleTrack{}
		}
		item.Subtitles = subtitles

		results = append(results, item)
	}

	return results, nil
}

func resolveMetadata(abs, root string, manifests map[string]*catalogFile, perFile map[string]catalogEntry) Metadata {
	dir := filepath.Dir(abs)
	base := filepath.Base(abs)

	// 1. Exact entry inside a directory catalog.json.
	if manifest, ok := manifests[dir]; ok {
		for _, entry := range manifest.Movies {
			if entry.File == base || entry.File == filepath.ToSlash(mustRel(root, abs)) {
				return metadataFromEntry(entry, abs, "CATALOG_JSON")
			}
		}
	}

	// 2. A per-file sidecar JSON named after the video.
	if entry, ok := perFile[strings.TrimSuffix(abs, filepath.Ext(abs))+".json"]; ok {
		return metadataFromEntry(entry, abs, "CATALOG_JSON")
	}

	// 3. Filename parsing.
	parsed := ParseFilename(base)
	return Metadata{
		Title:   parsed.Title,
		Year:    parsed.Year,
		Genres:  []string{"Umum"},
		Source:  "FILENAME",
		Quality: detectQuality(base),
		Raw:     json.RawMessage(`{}`),
	}
}

func metadataFromEntry(entry catalogEntry, abs, source string) Metadata {
	parsed := ParseFilename(filepath.Base(abs))

	title := strings.TrimSpace(entry.Title)
	if title == "" {
		title = parsed.Title
	}
	year := entry.Year
	if year == 0 {
		year = parsed.Year
	}
	genres := sanitiseGenres(entry.Genres)
	if len(genres) == 0 {
		genres = []string{"Umum"}
	}

	raw := entry.Raw
	if len(raw) == 0 {
		encoded, err := json.Marshal(entry)
		if err != nil {
			raw = json.RawMessage(`{}`)
		} else {
			raw = encoded
		}
	}

	return Metadata{
		Title:            TitleCase(title),
		Year:             year,
		Genres:           genres,
		Synopsis:         strings.TrimSpace(entry.Synopsis),
		Tagline:          strings.TrimSpace(entry.Tagline),
		Director:         strings.TrimSpace(entry.Director),
		Cast:             entry.Cast,
		Country:          strings.TrimSpace(entry.Country),
		OriginalLanguage: strings.TrimSpace(entry.OriginalLanguage),
		AgeRating:        strings.TrimSpace(entry.AgeRating),
		Quality:          firstNonEmpty(entry.Quality, detectQuality(filepath.Base(abs))),
		DurationSeconds:  entry.DurationSeconds,
		Source:           source,
		Raw:              raw,
		Poster:           entry.Poster,
		Banner:           entry.Banner,
		Subtitles:        entry.Subtitles,
	}
}

// findSidecars discovers artwork and subtitle files that share the video name.
func findSidecars(abs, root string) (posterRel, bannerRel string, subtitles []domain.SubtitleTrack) {
	dir := filepath.Dir(abs)
	base := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", nil
	}

	dirPoster := ""
	dirBanner := ""
	videoCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if IsVideo(entry.Name()) && !strings.HasSuffix(strings.ToLower(entry.Name()), ".ts") {
			videoCount++
		}
	}

	subtitles = make([]domain.SubtitleTrack, 0, 2)

	for _, entry := range entries {
		if entry.IsDir() || skipFile(entry.Name()) {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		full := filepath.Join(dir, name)
		rel := filepath.ToSlash(mustRel(root, full))

		switch {
		case IsSubtitle(name):
			if sharesNameBase(lower, base) {
				code, label := LanguageFromFilename(name)
				subtitles = append(subtitles, domain.SubtitleTrack{Language: code, Label: label, Path: rel})
			}

		case IsPoster(name) && sharesNameBase(lower, base):
			if strings.Contains(lower, "banner") || strings.Contains(lower, "backdrop") {
				if bannerRel == "" {
					bannerRel = rel
				}
			} else if posterRel == "" {
				posterRel = rel
			}

		case strings.EqualFold(name, "poster.jpg") || strings.EqualFold(name, "poster.png"),
			strings.EqualFold(name, "folder.jpg") || strings.EqualFold(name, "folder.png"):
			if videoCount == 1 && dirPoster == "" {
				dirPoster = rel
			}

		case strings.EqualFold(name, "banner.jpg") || strings.EqualFold(name, "banner.png"),
			strings.EqualFold(name, "backdrop.jpg") || strings.EqualFold(name, "backdrop.png"):
			if videoCount == 1 && dirBanner == "" {
				dirBanner = rel
			}
		}
	}

	if posterRel == "" {
		posterRel = dirPoster
	}
	if bannerRel == "" {
		bannerRel = dirBanner
	}

	sort.Slice(subtitles, func(i, j int) bool {
		if subtitles[i].Language == "id" {
			return true
		}
		if subtitles[j].Language == "id" {
			return false
		}
		return subtitles[i].Label < subtitles[j].Label
	})

	return posterRel, bannerRel, subtitles
}

func normaliseSidecar(videoAbs, root, declared string) string {
	if declared == "" {
		return ""
	}
	if filepath.IsAbs(declared) {
		return filepath.ToSlash(mustRel(root, declared))
	}
	candidate := filepath.Join(filepath.Dir(videoAbs), filepath.FromSlash(declared))
	if _, err := os.Stat(candidate); err == nil {
		return filepath.ToSlash(mustRel(root, candidate))
	}
	candidate = filepath.Join(root, filepath.FromSlash(declared))
	if _, err := os.Stat(candidate); err == nil {
		return filepath.ToSlash(mustRel(root, candidate))
	}
	return ""
}

func normaliseSubtitles(videoAbs, root string, declared []domain.SubtitleTrack) []domain.SubtitleTrack {
	out := make([]domain.SubtitleTrack, 0, len(declared))
	for _, sub := range declared {
		path := normaliseSidecar(videoAbs, root, sub.Path)
		if path == "" {
			continue
		}
		code := sub.Language
		if code == "" {
			code = "und"
		}
		label := sub.Label
		if label == "" {
			label = strings.ToUpper(code)
		}
		out = append(out, domain.SubtitleTrack{Language: code, Label: label, Path: path})
	}
	return out
}

func readCatalog(path string) (*catalogFile, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var wrapper catalogFile
	if err := json.Unmarshal(body, &wrapper); err == nil && len(wrapper.Movies) > 0 {
		return &wrapper, nil
	}

	var list []catalogEntry
	if err := json.Unmarshal(body, &list); err == nil && len(list) > 0 {
		return &catalogFile{Movies: list}, nil
	}

	return nil, fmt.Errorf("media: %s is not a recognised catalog", path)
}

func readCatalogEntry(path string) (*catalogEntry, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entry catalogEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		return nil, err
	}
	if entry.Title == "" && len(entry.Genres) == 0 && entry.Synopsis == "" {
		return nil, fmt.Errorf("media: %s has no usable metadata", path)
	}
	return &entry, nil
}

func sanitiseGenres(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, g := range in {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		key := strings.ToLower(g)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, g)
	}
	return out
}

func detectQuality(name string) string {
	lower := strings.ToLower(name)
	for _, token := range []string{"2160p", "4k", "1440p", "1080p", "720p", "576p", "480p"} {
		if strings.Contains(lower, token) {
			return token
		}
	}
	return ""
}

// sharesNameBase reports whether a sidecar belongs to a video file.
//
// Release names are inconsistent: the video carries quality tags while the
// artwork usually does not, and subtitles often add a language token. The match
// therefore compares normalised tokens and accepts a prefix relationship in
// either direction, with a minimum length so "a.jpg" cannot match everything.
func sharesNameBase(sidecarName, videoBase string) bool {
	stem := strings.TrimSuffix(sidecarName, filepath.Ext(sidecarName))
	stem = stripLanguageToken(stem)

	normalise := func(s string) string {
		replacer := strings.NewReplacer(".", " ", "_", " ", "-", " ", "(", " ", ")", " ", "[", " ", "]", " ")
		return multiSpace.ReplaceAllString(strings.TrimSpace(replacer.Replace(strings.ToLower(s))), " ")
	}

	stemNorm := normalise(stem)
	baseNorm := normalise(videoBase)
	if stemNorm == "" || baseNorm == "" {
		return false
	}
	if len(stemNorm) < 4 || len(baseNorm) < 4 {
		return false
	}
	return strings.HasPrefix(baseNorm, stemNorm) || strings.HasPrefix(stemNorm, baseNorm)
}

// stripLanguageToken removes a trailing language marker such as ".id" or
// ".indonesia" so "film.id.srt" still pairs with "film.mkv".
func stripLanguageToken(stem string) string {
	tokens := strings.FieldsFunc(stem, func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == ' '
	})
	if len(tokens) < 2 {
		return stem
	}
	last := tokens[len(tokens)-1]
	switch last {
	case "id", "ind", "indo", "indonesia", "idn",
		"en", "eng", "english", "inggris",
		"ms", "may", "melayu", "zh", "chi", "chinese", "mandarin",
		"ar", "ara", "arab", "ja", "jpn", "japanese", "ko", "kor", "korean":
		return strings.Join(tokens[:len(tokens)-1], " ")
	}
	return stem
}

func skipDir(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch lower {
	case "@eadir", "#recycle", "$recycle.bin", "system volume information",
		"lost+found", "thumbnails", ".thumbnails", "temp", "tmp":
		return true
	}
	return false
}

func skipFile(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "._") {
		return true
	}
	lower := strings.ToLower(name)
	switch lower {
	case "thumbs.db", "desktop.ini", "autorun.inf", ".ds_store":
		return true
	}
	return false
}

func depth(rel string) int {
	return strings.Count(filepath.ToSlash(rel), "/")
}

func mustRel(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return rel
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
