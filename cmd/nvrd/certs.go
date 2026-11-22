package main

import (
	"os"
	"strings"
)

// nvrd is built with GOOS=linux, so Go looks for CA certificates in
// /etc/ssl/certs and friends — none of which exist on Android. Without this,
// every outbound HTTPS call from the phone (Web Push to FCM/APNs, ntfy,
// Telegram) fails with "x509: certificate signed by unknown authority".
// Point Go at Android's own trust store before the first TLS handshake. An
// explicit SSL_CERT_FILE / SSL_CERT_DIR from the environment still wins.
func init() {
	if os.Getenv("SSL_CERT_FILE") != "" || os.Getenv("SSL_CERT_DIR") != "" {
		return
	}
	var dirs []string
	for _, d := range []string{
		"/system/etc/security/cacerts",        // all Android versions
		"/apex/com.android.conscrypt/cacerts", // Android 14+ (updatable)
	} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) > 0 {
		_ = os.Setenv("SSL_CERT_DIR", strings.Join(dirs, ":"))
	}
}
