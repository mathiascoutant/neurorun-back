package models

import "strings"

// Plateformes déduites du libellé d’appareil d’une session.
const (
	PlatformIOS     = "ios"
	PlatformAndroid = "android"
	PlatformWeb     = "web"
	// PlatformUnknown : libellé vide ou non reconnu. Jamais masqué dans la console — un
	// classement muet sur ce qu’il ne sait pas ranger donnerait des chiffres faux sans le dire.
	PlatformUnknown = "unknown"
)

// PlatformOrder : ordre d’affichage stable, du plus au moins attendu.
var PlatformOrder = []string{PlatformIOS, PlatformAndroid, PlatformWeb, PlatformUnknown}

// PlatformFromDevice classe une session à partir du libellé stocké sur son jeton de
// rafraîchissement (`deviceLabel`), qui retombe sur le User-Agent faute d’en-tête dédié.
//
// C’est une heuristique, pas une vérité : l’app n’annonce pas sa plateforme, on la déduit de
// ce que la pile réseau met dans le User-Agent. NSURLSession (iOS) signe « CFNetwork » et
// « Darwin », OkHttp (Android) signe « okhttp », un navigateur « Mozilla ». L’ordre des tests
// compte : un navigateur iOS dit à la fois Mozilla et CFNetwork, et c’est bien du web.
func PlatformFromDevice(device string) string {
	d := strings.ToLower(strings.TrimSpace(device))
	if d == "" {
		return PlatformUnknown
	}
	// Un navigateur se déclare toujours « Mozilla/5.0 » en tête ; les clients natifs, jamais.
	if strings.HasPrefix(d, "mozilla/") {
		return PlatformWeb
	}
	if strings.Contains(d, "cfnetwork") || strings.Contains(d, "darwin") {
		return PlatformIOS
	}
	if strings.Contains(d, "okhttp") || strings.Contains(d, "android") || strings.Contains(d, "dalvik") {
		return PlatformAndroid
	}
	if strings.Contains(d, "mozilla") || strings.Contains(d, "chrome") || strings.Contains(d, "safari") {
		return PlatformWeb
	}
	return PlatformUnknown
}

// IsIOSDevice : raccourci de lecture pour la notification de première installation.
func IsIOSDevice(device string) bool {
	return PlatformFromDevice(device) == PlatformIOS
}
