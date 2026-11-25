package media

import "testing"

func TestParseFilename(t *testing.T) {
	cases := []struct {
		filename  string
		wantTitle string
		wantYear  int
	}{
		{
			filename:  "Laskar_Pelangi.2008.1080p.WEB-DL.x264.mkv",
			wantTitle: "Laskar Pelangi",
			wantYear:  2008,
		},
		{
			filename:  "inception (2010) [BluRay] 720p.mp4",
			wantTitle: "Inception",
			wantYear:  2010,
		},
		{
			filename:  "Pengabdi Setan 2 Communion 2022 2160p.mkv",
			wantTitle: "Pengabdi Setan 2 Communion",
			wantYear:  2022,
		},
		{
			filename:  "Film Indonesia Terbaru.mkv",
			wantTitle: "Film Indonesia Terbaru",
			wantYear:  0,
		},
		{
			filename:  "the.grand.budapest.hotel.2014.1080p.bluray.x265.dts.mkv",
			wantTitle: "The Grand Budapest Hotel",
			wantYear:  2014,
		},
		{
			filename:  "AVENGERS_ENDGAME_2019_1080p.mp4",
			wantTitle: "AVENGERS ENDGAME",
			wantYear:  2019,
		},
	}

	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			got := ParseFilename(tc.filename)
			if got.Title != tc.wantTitle {
				t.Errorf("title = %q, want %q", got.Title, tc.wantTitle)
			}
			if got.Year != tc.wantYear {
				t.Errorf("year = %d, want %d", got.Year, tc.wantYear)
			}
		})
	}
}

func TestParseFilenameNeverReturnsEmptyTitle(t *testing.T) {
	for _, name := range []string{"1080p.mkv", ".mkv", "x264.mp4"} {
		got := ParseFilename(name)
		if got.Title == "" {
			t.Fatalf("%q produced an empty title", name)
		}
	}
}

func TestSlug(t *testing.T) {
	cases := []struct {
		title string
		year  int
		want  string
	}{
		{"Laskar Pelangi", 2008, "laskar-pelangi-2008"},
		{"Inception", 0, "inception"},
		{"Film 100% Indonesia!", 2020, "film-100-indonesia-2020"},
		{"", 0, "movie"},
	}

	for _, tc := range cases {
		if got := Slug(tc.title, tc.year); got != tc.want {
			t.Errorf("Slug(%q, %d) = %q, want %q", tc.title, tc.year, got, tc.want)
		}
	}
}

func TestLanguageFromFilename(t *testing.T) {
	cases := []struct {
		filename string
		wantCode string
	}{
		{"Laskar_Pelangi.id.srt", "id"},
		{"Laskar_Pelangi.indonesia.srt", "id"},
		{"Laskar_Pelangi.en.srt", "en"},
		{"Laskar_Pelangi.english.vtt", "en"},
		{"Laskar_Pelangi.srt", "und"},
	}

	for _, tc := range cases {
		code, label := LanguageFromFilename(tc.filename)
		if code != tc.wantCode {
			t.Errorf("%q -> code %q, want %q", tc.filename, code, tc.wantCode)
		}
		if label == "" {
			t.Errorf("%q produced an empty label", tc.filename)
		}
	}
}

func TestIsVideoAndIsSubtitle(t *testing.T) {
	videos := []string{"a.mp4", "b.MKV", "c.m3u8", "d.webm"}
	for _, name := range videos {
		if !IsVideo(name) {
			t.Errorf("%q should be a video", name)
		}
	}

	notVideos := []string{"a.txt", "b.jpg", "c.json", "d.srt"}
	for _, name := range notVideos {
		if IsVideo(name) {
			t.Errorf("%q should not be a video", name)
		}
	}

	subs := []string{"a.srt", "b.VTT", "c.ass"}
	for _, name := range subs {
		if !IsSubtitle(name) {
			t.Errorf("%q should be a subtitle", name)
		}
	}
}

func TestContainer(t *testing.T) {
	cases := map[string]string{
		"film.MP4":       "mp4",
		"film.mkv":       "mkv",
		"path/film.m3u8": "m3u8",
	}
	for input, want := range cases {
		if got := Container(input); got != want {
			t.Errorf("Container(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTitleCaseKeepsAcronyms(t *testing.T) {
	cases := map[string]string{
		"laskar pelangi": "Laskar Pelangi",
		"the 3d movie":   "The 3d Movie",
		"film ii":        "Film Ii",
		"":               "",
	}
	for input, want := range cases {
		if got := TitleCase(input); got != want {
			t.Errorf("TitleCase(%q) = %q, want %q", input, got, want)
		}
	}
}
