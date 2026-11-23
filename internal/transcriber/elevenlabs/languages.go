package elevenlabs

import "strings"

// iso6393To1 maps the ISO 639-3 codes Scribe returns to the ISO 639-1 codes
// used elsewhere in the service (the whisper language set).
var iso6393To1 = map[string]string{
	"afr": "af", "amh": "am", "ara": "ar", "asm": "as", "aze": "az",
	"bak": "ba", "bel": "be", "ben": "bn", "bod": "bo", "bos": "bs",
	"bre": "br", "bul": "bg", "cat": "ca", "ces": "cs", "cmn": "zh",
	"cym": "cy", "dan": "da", "deu": "de", "ell": "el", "eng": "en",
	"est": "et", "eus": "eu", "fao": "fo", "fas": "fa", "fil": "tl",
	"fin": "fi", "fra": "fr", "glg": "gl", "guj": "gu", "hat": "ht",
	"hau": "ha", "haw": "haw", "heb": "he", "hin": "hi", "hrv": "hr",
	"hun": "hu", "hye": "hy", "ind": "id", "isl": "is", "ita": "it",
	"jav": "jw", "jpn": "ja", "kan": "kn", "kat": "ka", "kaz": "kk",
	"khm": "km", "kor": "ko", "lao": "lo", "lat": "la", "lav": "lv",
	"lin": "ln", "lit": "lt", "ltz": "lb", "mal": "ml", "mar": "mr",
	"mkd": "mk", "mlg": "mg", "mlt": "mt", "mon": "mn", "mri": "mi",
	"msa": "ms", "mya": "my", "nep": "ne", "nld": "nl", "nno": "nn",
	"nob": "no", "nor": "no", "oci": "oc", "pan": "pa", "pol": "pl",
	"por": "pt", "pus": "ps", "ron": "ro", "rus": "ru", "san": "sa",
	"sin": "si", "slk": "sk", "slv": "sl", "sna": "sn", "snd": "sd",
	"som": "so", "spa": "es", "sqi": "sq", "srp": "sr", "sun": "su",
	"swa": "sw", "swe": "sv", "tam": "ta", "tat": "tt", "tel": "te",
	"tgk": "tg", "tgl": "tl", "tha": "th", "tuk": "tk", "tur": "tr",
	"ukr": "uk", "urd": "ur", "uzb": "uz", "vie": "vi", "yid": "yi",
	"yor": "yo", "yue": "yue", "zho": "zh",
}

// toISO6391 returns "" for unknown codes so the caller can fall back.
func toISO6391(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return ""
	}
	if v, ok := iso6393To1[code]; ok {
		return v
	}
	if len(code) == 2 {
		return code
	}
	return ""
}
