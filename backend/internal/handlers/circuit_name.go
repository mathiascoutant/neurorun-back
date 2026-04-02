package handlers

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// blockedCircuitNameTokens : mots ou fragments à refuser (minuscules, sans accents pour comparaison).
var blockedCircuitNameTokens = []string{
	"connard", "connasse", "encule", "enculé", "fdp", "merde", "nazi", "hitler",
	"pute", "salope", "negre", "nègre", "pd ", "pédé", "pedo", "pédoph",
	"fuck", "shit", "nigger", "bitch", "sex", "porn", "xxx",
}

func stripAccentsLower(s string) string {
	s = norm.NFKC.String(s)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case 'à', 'á', 'â', 'ã', 'ä', 'å':
			r = 'a'
		case 'è', 'é', 'ê', 'ë':
			r = 'e'
		case 'ì', 'í', 'î', 'ï':
			r = 'i'
		case 'ò', 'ó', 'ô', 'õ', 'ö':
			r = 'o'
		case 'ù', 'ú', 'û', 'ü':
			r = 'u'
		case 'ç':
			r = 'c'
		case 'ñ':
			r = 'n'
		case 'æ':
			b.WriteString("ae")
			continue
		case 'œ':
			b.WriteString("oe")
			continue
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SanitizeCircuitName valide et normalise le nom d’un parcours. Retourne "" et un message d’erreur si refusé.
func SanitizeCircuitName(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "nom requis"
	}
	if utf8.RuneCountInString(raw) > 48 {
		return "", "nom trop long (48 caractères max)"
	}
	nfk := norm.NFKC.String(raw)
	var out strings.Builder
	var prevSpace bool
	for _, r := range nfk {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
			prevSpace = false
			continue
		}
		switch r {
		case ' ', '-', '\'', '.', ',', '’':
			if out.Len() == 0 || prevSpace {
				continue
			}
			out.WriteRune(' ')
			prevSpace = true
		default:
			return "", "caractères non autorisés (lettres, chiffres, espaces, - ' . ,)"
		}
	}
	s := strings.TrimSpace(out.String())
	if utf8.RuneCountInString(s) < 2 {
		return "", "nom trop court (2 caractères min)"
	}
	low := stripAccentsLower(s)
	for _, tok := range blockedCircuitNameTokens {
		if strings.Contains(" "+low+" ", " "+tok) {
			return "", "nom non autorisé"
		}
	}
	return s, ""
}
