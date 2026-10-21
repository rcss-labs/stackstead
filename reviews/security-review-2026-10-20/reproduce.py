#!/usr/bin/env python3
"""Reproduce audit findings using Go overlays, temporary data, and loopback only.

Run from any directory with Python 3 and the repository's Go toolchain installed.
The security assertions intentionally fail on the audited vulnerable baseline.
Application files are never edited. No Docker daemon or real credentials are used.
"""

import json
from pathlib import Path
import subprocess
import tempfile


AUTH_TEST = r'''
func TestSecurityReviewLoginAfterPasswordRecovery(t *testing.T) {
    for _, recovery := range []string{"change", "reset"} {
        t.Run(recovery, func(t *testing.T) {
            svc, _, _ := newTestService(t)
            ctx := context.Background()
            if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil { t.Fatal(err) }
            keep, err := svc.Login(ctx, "admin", goodPassword)
            if err != nil { t.Fatal(err) }
            user, err := svc.Authenticate(ctx, keep)
            if err != nil { t.Fatal(err) }
            original := deriveKey
            defer func() { deriveKey = original }()
            recovered := false
            deriveKey = func(password, salt []byte, iterations, memory uint32, threads uint8, keyLen uint32) []byte {
                if !recovered && string(password) == goodPassword {
                    recovered = true
                    // Login has read the old hash and owns no DB connection.
                    // Commit real password recovery before its verification returns.
                    if recovery == "change" {
                        err = svc.ChangePassword(ctx, user.ID, goodPassword, "replacement synthetic password", keep)
                    } else {
                        _, err = svc.ResetPassword(ctx, "admin", "replacement synthetic password")
                    }
                    if err != nil { t.Fatal(err) }
                }
                return original(password, salt, iterations, memory, threads, keyLen)
            }
            late, err := svc.Login(ctx, "admin", goodPassword)
            if !recovered { t.Fatal("fixture did not reach password verification") }
            if err == nil {
                if _, authErr := svc.Authenticate(ctx, late); authErr == nil {
                    t.Fatal("SEC-01 reproduced: old-password login minted a valid session after recovery committed")
                }
            } else if !errors.Is(err, ErrInvalidCredentials) { t.Fatal(err) }
        })
    }
}
'''

TLS_TEST = r'''
func TestSecurityReviewPinnedIPRejectsUnrelatedTrustedCertificate(t *testing.T) {
    for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
        t.Run(fmt.Sprintf("TLS-%x", version), func(t *testing.T) {
            key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
            if err != nil { t.Fatal(err) }
            template := &x509.Certificate{
                SerialNumber: big.NewInt(1),
                DNSNames: []string{"unrelated.example.invalid"},
                NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
                IsCA: true, BasicConstraintsValid: true,
                KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
                ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
            }
            der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
            if err != nil { t.Fatal(err) }
            srv := httptest.NewUnstartedServer(jsonHandler(`{"ok":true}`))
            srv.TLS = &tls.Config{MinVersion: version, MaxVersion: version,
                Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
            srv.StartTLS()
            defer srv.Close()
            base, _ := url.Parse(srv.URL)
            roots := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
            // Control: the normal verifier must reject this IP/name mismatch.
            normal, err := NewHTTPClient(base, TLSOptions{CAPEM: roots}, time.Second)
            if err != nil { t.Fatal(err) }
            defer normal.CloseIdleConnections()
            res, err := normal.Get(base.String())
            if res != nil { res.Body.Close() }
            if err == nil { t.Fatal("fixture invalid: normal verifier accepted unrelated certificate") }
            // Same roots, unrelated certificate, and a pin that does not match.
            pinned, err := NewHTTPClient(base, TLSOptions{CAPEM: roots, PinSHA256: strings.Repeat("ab",32)}, time.Second)
            if err != nil { t.Fatal(err) }
            defer pinned.CloseIdleConnections()
            res, err = pinned.Get(base.String())
            if res != nil { res.Body.Close() }
            if err == nil { t.Fatal("SEC-02 reproduced: IP peer accepted unrelated trusted certificate despite wrong pin") }
        })
    }
}
'''

API_TEST = r'''
func TestSecurityReviewRevokedSessionCannotKeepEventStream(t *testing.T) {
    for _, action := range []string{"logout", "password-change", "expiry"} {
        t.Run(action, func(t *testing.T) {
            fake := newFakeObserve()
            env, cookie := newObserveEnv(t, fake)
            ctx, cancel := context.WithCancel(context.Background())
            defer cancel()
            _, events := openSSE(t, env, cookie, ctx, "/api/events")
            if ev := nextEvent(t, events, true); ev.name != "status" { t.Fatal("missing initial status") }
            switch action {
            case "logout":
                if rec := env.do(t, "POST", "/api/logout", "", withCookie(cookie)); rec.Code != 204 { t.Fatal(rec.Code) }
            case "password-change":
                owner := env.login(t)
                user, err := env.auth.Authenticate(ctx, owner.Value)
                if err != nil { t.Fatal(err) }
                if err = env.auth.ChangePassword(ctx, user.ID, adminPassword, "replacement synthetic password", owner.Value); err != nil { t.Fatal(err) }
            case "expiry":
                env.clock.Advance(8 * 24 * time.Hour)
            }
            if rec := env.do(t, "GET", "/api/me", "", withCookie(cookie)); rec.Code != 401 {
                t.Fatalf("fixture failed to invalidate session: status %d", rec.Code)
            }
            fake.changes <- state.Change{Project: "after-revocation", Seq: 987}
            timeout := time.After(time.Second)
            for {
                select {
                case ev, ok := <-events:
                    if !ok { return }
                    if ev.name == "change" { t.Fatal("SEC-03 reproduced: invalid session still receives new events") }
                case <-timeout:
                    t.Fatal("SEC-03 reproduced: invalid session stream remained open")
                }
            }
        })
    }
}

func TestSecurityReviewProxyClientCannotLockOutOtherClients(t *testing.T) {
    env := newTestEnv(t, "")
    env.createAdmin(t)
    for i := 0; i < 5; i++ {
        rec := env.do(t, "POST", "/api/login", `{"username":"admin","password":"wrong synthetic password"}`,
            fromLoopback, withHeader("X-Forwarded-For", "198.51.100.1"))
        if rec.Code != 401 { t.Fatalf("fixture wrong-login status %d", rec.Code) }
    }
    rec := env.do(t, "POST", "/api/login", fmt.Sprintf(`{"username":"admin","password":%q}`, adminPassword),
        fromLoopback, withHeader("X-Forwarded-For", "198.51.100.2"))
    if rec.Code != 204 {
        t.Fatalf("SEC-04 reproduced: unrelated proxy client with correct password received %d", rec.Code)
    }
}
'''


def main():
    root = Path(__file__).resolve().parents[2]
    cases = [
        ("internal/auth/service_test.go", AUTH_TEST, ""),
        ("internal/peers/transport_test.go", TLS_TEST,
         '\n"crypto/ecdsa"\n"crypto/elliptic"\n"crypto/rand"\n"crypto/tls"\n"fmt"\n"math/big"\n'),
        ("internal/api/observe_test.go", API_TEST, ""),
    ]
    with tempfile.TemporaryDirectory(prefix="stackstead-security-review-") as temp:
        replacements = {}
        for index, (relative, extra, imports) in enumerate(cases):
            original = root / relative
            content = original.read_text()
            if imports:
                content = content.replace("import (", "import (" + imports, 1)
            replacement = Path(temp) / f"case{index}_test.go"
            replacement.write_text(content + extra)
            replacements[str(original)] = str(replacement)
        overlay = Path(temp) / "overlay.json"
        overlay.write_text(json.dumps({"Replace": replacements}))
        command = ["go", "test", "-overlay", str(overlay), "-count=1", "-timeout", "60s",
                   "-run", "^TestSecurityReview", "-v", "./internal/auth", "./internal/peers", "./internal/api"]
        print("Running security assertions on temporary overlays; failures identify reproduced findings.", flush=True)
        return subprocess.run(command, cwd=root, check=False).returncode


if __name__ == "__main__":
    raise SystemExit(main())
