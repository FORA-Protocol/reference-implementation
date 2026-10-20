package testutil

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"time"
)

// A key directory is only ever fetched over https, and the services build their
// directory-fetch clients themselves, from the SDK's guarded factory, with the
// default TLS roots. A test that drives such a client against an httptest TLS
// server therefore needs the process to trust that server's certificate.

// TrustHTTPTestTLS makes every client in this test process trust the
// certificate every httptest TLS server presents (valid for 127.0.0.1, ::1 and
// example.com), in place of the platform's roots. It must run before anything
// in the process has verified a TLS certificate — at the top of TestMain — and
// it takes effect only in a test binary whose package declares
//
//	//go:debug x509usefallbackroots=1
//
// in one of its _test.go files: without that setting Go keeps preferring the
// platform's roots. Production code is untouched: the setting and the roots
// exist only in the test binary.
func TrustHTTPTestTLS() {
	x509.SetFallbackRoots(httptestRoots())
}

// HTTPTestTLSClient is a client that trusts the certificate every httptest TLS
// server presents, for a test that injects the client a key directory is
// fetched with. timeout bounds one request.
func HTTPTestTLSClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: httptestRoots(), MinVersion: tls.VersionTLS12},
			DisableKeepAlives: true,
		},
	}
}

// httptestRoots is a pool holding the certificate httptest's TLS servers share.
func httptestRoots() *x509.CertPool {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return pool
}
