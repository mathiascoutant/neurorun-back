package appstore

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Extrait d’un rapport SALES/SUMMARY : colonnes réelles, dans l’ordre réel.
const sampleTSV = "Provider\tProvider Country\tSKU\tDeveloper\tTitle\tVersion\tProduct Type Identifier\tUnits\tDeveloper Proceeds\tBegin Date\tEnd Date\tCustomer Currency\tCountry Code\n" +
	"APPLE\tFR\tneurorun\tVISIONSHOW\tNeuroRun\t1.0.1\t1F\t12\t0\t09/20/2026\t09/20/2026\tEUR\tFR\n" +
	"APPLE\tFR\tneurorun\tVISIONSHOW\tNeuroRun\t1.0.1\t1F\t3\t0\t09/20/2026\t09/20/2026\tEUR\tBE\n" +
	"APPLE\tFR\tneurorun\tVISIONSHOW\tNeuroRun\t1.0.1\t3F\t5\t0\t09/20/2026\t09/20/2026\tEUR\tFR\n" +
	"APPLE\tFR\tneurorun\tVISIONSHOW\tNeuroRun\t1.0.1\t7F\t40\t0\t09/20/2026\t09/20/2026\tEUR\tFR\n" +
	"APPLE\tFR\tneurorun\tVISIONSHOW\tNeuroRun\t1.0.1\tIA1\t2\t2.79\t09/20/2026\t09/20/2026\tEUR\tFR\n"

func TestParseSalesTSV(t *testing.T) {
	got, err := ParseSalesTSV(strings.NewReader(sampleTSV))
	if err != nil {
		t.Fatalf("ParseSalesTSV : %v", err)
	}
	if got.FirstInstalls != 15 {
		t.Errorf("premiers téléchargements = %d, attendu 15", got.FirstInstalls)
	}
	if got.Redownloads != 5 {
		t.Errorf("réinstallations = %d, attendu 5", got.Redownloads)
	}
	if got.Updates != 40 {
		t.Errorf("mises à jour = %d, attendu 40", got.Updates)
	}
	if got.InstallsByCountry["FR"] != 12 || got.InstallsByCountry["BE"] != 3 {
		t.Errorf("par pays = %v, attendu FR:12 BE:3", got.InstallsByCountry)
	}
	// Un achat intégré n’est ni un téléchargement ni une mise à jour, mais il doit rester
	// visible dans le détail brut : c’est ce qui permet de vérifier le classement.
	if got.UnitsByType["IA1"] != 2 {
		t.Errorf("codes bruts = %v, attendu IA1:2", got.UnitsByType)
	}
}

// Apple ajoute des colonnes au fil des versions de rapport : les repérer par nom et non par
// position est ce qui évite un décalage silencieux.
func TestParseSalesTSVIgnoresColumnOrder(t *testing.T) {
	tsv := "Units\tCountry Code\tProduct Type Identifier\n" +
		"7\tFR\t1\n" +
		"2\tDE\t1T\n"
	got, err := ParseSalesTSV(strings.NewReader(tsv))
	if err != nil {
		t.Fatalf("ParseSalesTSV : %v", err)
	}
	if got.FirstInstalls != 9 {
		t.Errorf("premiers téléchargements = %d, attendu 9", got.FirstInstalls)
	}
}

func TestParseSalesTSVRejectsUnexpectedReport(t *testing.T) {
	if _, err := ParseSalesTSV(strings.NewReader("Foo\tBar\n1\t2\n")); err == nil {
		t.Fatal("un rapport sans les colonnes attendues doit remonter une erreur")
	}
}

// testKeyPEM : clé EC P-256 de test, générée pour ce test et pour lui seul.
// Elle ne donne accès à rien — elle sert à vérifier la signature, pas à joindre Apple.
func testKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("génération de clé : %v", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("encodage de clé : %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

// Le jeton doit porter l’algorithme, le `kid` et l’audience qu’Apple exige : une erreur sur
// l’un des trois se solde par un 401 sans explication utile.
func TestClientTokenClaims(t *testing.T) {
	c, err := New("issuer-de-test", "KEYID12345", testKeyPEM(t), "80000000", "")
	if err != nil {
		t.Fatalf("New : %v", err)
	}
	raw, err := c.token()
	if err != nil {
		t.Fatalf("token : %v", err)
	}

	parsed, _, err := jwt.NewParser().ParseUnverified(raw, jwt.MapClaims{})
	if err != nil {
		t.Fatalf("jeton illisible : %v", err)
	}
	if parsed.Method.Alg() != "ES256" {
		t.Errorf("alg = %q, attendu ES256", parsed.Method.Alg())
	}
	if kid, _ := parsed.Header["kid"].(string); kid != "KEYID12345" {
		t.Errorf("kid = %q, attendu KEYID12345", kid)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if claims["aud"] != "appstoreconnect-v1" {
		t.Errorf("aud = %v, attendu appstoreconnect-v1", claims["aud"])
	}
	if claims["iss"] != "issuer-de-test" {
		t.Errorf("iss = %v, attendu issuer-de-test", claims["iss"])
	}
	exp, _ := claims["exp"].(float64)
	iat, _ := claims["iat"].(float64)
	if d := time.Duration(exp-iat) * time.Second; d <= 0 || d > 20*time.Minute {
		t.Errorf("durée de vie = %v, Apple refuse au-delà de 20 minutes", d)
	}
}

func TestNewRejectsInvalidKey(t *testing.T) {
	if _, err := New("issuer", "kid", []byte("pas une clé"), "80000000", "1_1"); err == nil {
		t.Fatal("une clé illisible doit remonter une erreur explicite")
	}
}
