package leaderboard

import "strings"

// Codes ISO 3166-1 alpha-2 reconnus. Le pays est choisi dans une liste par le
// coureur : tout autre code est une erreur du client.
const isoCodes = "AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ " +
	"CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR " +
	"GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP " +
	"KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ " +
	"NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW " +
	"SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ " +
	"UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS XK YE YT ZA ZM ZW"

// Europe : pays du continent, Russie et Turquie comprises, plus les territoires
// rattachés (îles Féroé, Gibraltar…). Les DOM-TOM ont leur propre code et n'y
// sont pas : un coureur réunionnais choisit RE.
const europeCodes = "AD AL AM AT AX AZ BA BE BG BY CH CY CZ DE DK EE ES FI FO FR GB GE GG GI GR HR HU IE IM IS IT JE " +
	"LI LT LU LV MC MD ME MK MT NL NO PL PT RO RS RU SE SI SJ SK SM TR UA VA XK"

var (
	validCountries = toSet(isoCodes)
	europe         = toSet(europeCodes)
)

func toSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, c := range strings.Fields(s) {
		out[c] = true
	}
	return out
}

// NormalizeCountry : « fr » → « FR ». Faux si le code n'existe pas.
func NormalizeCountry(code string) (string, bool) {
	c := strings.ToUpper(strings.TrimSpace(code))
	return c, validCountries[c]
}

// IsEurope : le pays compte pour le classement Europe.
func IsEurope(code string) bool { return europe[code] }

// EuropeCountries : la liste, pour filtrer en base.
func EuropeCountries() []string { return strings.Fields(europeCodes) }
