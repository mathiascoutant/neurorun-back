package models

import "testing"

func TestPlatformFromDevice(t *testing.T) {
	cases := []struct {
		device string
		want   string
	}{
		// Ce que NSURLSession envoie depuis une app React Native sur iPhone.
		{"NeuroRun/1.0.1 CFNetwork/1568.100.1 Darwin/24.0.0", PlatformIOS},
		{"Expo/2.33.20 CFNetwork/1494.0.7 Darwin/23.4.0", PlatformIOS},
		{"okhttp/4.12.0", PlatformAndroid},
		{"Dalvik/2.1.0 (Linux; U; Android 14)", PlatformAndroid},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/131.0", PlatformWeb},
		// Safari iOS : dit Darwin et CFNetwork sous le capot, mais reste un navigateur.
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15", PlatformWeb},
		{"", PlatformUnknown},
		{"curl/8.4.0", PlatformUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.device, func(t *testing.T) {
			if got := PlatformFromDevice(tc.device); got != tc.want {
				t.Fatalf("PlatformFromDevice(%q) = %q, attendu %q", tc.device, got, tc.want)
			}
		})
	}
}

func TestIsIOSDevice(t *testing.T) {
	if !IsIOSDevice("NeuroRun/1.0.1 CFNetwork/1568.100.1 Darwin/24.0.0") {
		t.Fatal("une session app iOS doit être reconnue")
	}
	if IsIOSDevice("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15") {
		t.Fatal("Safari sur iPhone n’est pas une installation de l’app")
	}
}
