package language

import "strings"

var aliases = map[string]string{
	"en": "en", "eng": "en", "enm": "en", "english": "en", "ingles": "en", "inglês": "en",
	"fr": "fr", "fra": "fr", "fre": "fr", "french": "fr", "frances": "fr", "francês": "fr", "français": "fr",
	"es": "es", "spa": "es", "spanish": "es", "espanhol": "es", "español": "es",
	"it": "it", "ita": "it", "italian": "it", "italiano": "it",
	"de": "de", "deu": "de", "ger": "de", "german": "de", "alemao": "de", "alemão": "de",
	"pt": "pt", "por": "pt", "portuguese": "pt", "portugues": "pt", "português": "pt",
	"ja": "ja", "jpn": "ja", "japanese": "ja", "japones": "ja", "japonês": "ja",
	"zh": "zh", "zho": "zh", "chi": "zh", "chinese": "zh", "chines": "zh", "chinês": "zh",
	"ko": "ko", "kor": "ko", "korean": "ko", "coreano": "ko",
	"ar": "ar", "ara": "ar", "arabic": "ar", "arabe": "ar", "árabe": "ar",
	"ru": "ru", "rus": "ru", "russian": "ru", "russo": "ru",
	"nl": "nl", "nld": "nl", "dut": "nl", "dutch": "nl", "holandes": "nl", "holandês": "nl",
	"hi": "hi", "hin": "hi", "hindi": "hi",
	"tr": "tr", "tur": "tr", "turkish": "tr", "turco": "tr",
	"pl": "pl", "pol": "pl", "polish": "pl", "polones": "pl", "polonês": "pl",
	"sv": "sv", "swe": "sv", "swedish": "sv", "sueco": "sv",
	"da": "da", "dan": "da", "danish": "da", "dinamarques": "da", "dinamarquês": "da",
	"fi": "fi", "fin": "fi", "finnish": "fi", "finlandes": "fi", "finlandês": "fi",
	"no": "no", "nor": "no", "nob": "no", "nno": "no", "norwegian": "no", "noruegues": "no", "norueguês": "no",
	"cs": "cs", "ces": "cs", "cze": "cs", "czech": "cs", "tcheco": "cs",
	"el": "el", "ell": "el", "gre": "el", "greek": "el", "grego": "el",
	"he": "he", "heb": "he", "hebrew": "he", "hebraico": "he",
	"id": "id", "ind": "id", "indonesian": "id", "indonesio": "id", "indonésio": "id",
	"vi": "vi", "vie": "vi", "vietnamese": "vi", "vietnamita": "vi",
	"th": "th", "tha": "th", "thai": "th", "tailandes": "th", "tailandês": "th",
	"uk": "uk", "ukr": "uk", "ukrainian": "uk", "ucraniano": "uk",
	"ca": "ca", "cat": "ca", "catalan": "ca", "catalao": "ca", "catalão": "ca",
	"eu": "eu", "eus": "eu", "baq": "eu", "basque": "eu", "basco": "eu",
	"ro": "ro", "ron": "ro", "rum": "ro", "romanian": "ro", "romeno": "ro",
	"hr": "hr", "hrv": "hr", "croatian": "hr", "croata": "hr",
	"hu": "hu", "hun": "hu", "hungarian": "hu", "hungaro": "hu", "húngaro": "hu",
	"ms": "ms", "msa": "ms", "may": "ms", "malay": "ms", "malaio": "ms",
	"fil": "fil", "tgl": "fil", "filipino": "fil",
	"gl": "gl", "glg": "gl", "galician": "gl", "galego": "gl",
}

// Canonical returns a stable ISO 639-1-style code when known. Region tags are
// intentionally reduced because embedded subtitle metadata usually uses ISO
// 639-2 and does not preserve regions.
func Canonical(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "_", "-")))
	if value == "auto" {
		return "auto"
	}
	if base, _, found := strings.Cut(value, "-"); found {
		value = base
	}
	if canonical, ok := aliases[value]; ok {
		return canonical
	}
	return value
}

// ParseOrdered accepts a user-friendly comma, semicolon or arrow-separated
// fallback list and removes duplicates without changing priority.
func ParseOrdered(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '>' || r == '→'
	})
	result := make([]string, 0, len(parts))
	seen := make(map[string]bool)
	for _, part := range parts {
		canonical := Canonical(part)
		if canonical == "" || seen[canonical] {
			continue
		}
		seen[canonical] = true
		result = append(result, canonical)
	}
	return result
}

func FormatOrdered(languages []string) string {
	if len(languages) == 0 {
		return "auto"
	}
	return strings.Join(languages, " → ")
}
