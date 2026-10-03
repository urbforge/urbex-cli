// A minimal service for the end-to-end test: it says which version and
// environment it is, so the scenario can tell what is deployed where, and
// has an endpoint only users with the Keycloak role "member" may call.
package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const version = "v1"

func main() {
	// Public: anyone.
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Logged to stdout: the scenario looks for these lines in Loki.
		log.Printf("hello-app %s served %s", version, r.URL.Path)
		fmt.Fprintf(w, "hello %s from urbex (%s)\n", version, os.Getenv("GREETING_ENV"))
	})
	// Members only: a token from the environment's Keycloak realm with the
	// realm role "member" (urbex gives the service KEYCLOAK_ISSUER and
	// KEYCLOAK_JWKS_URL).
	http.HandleFunc("/member", func(w http.ResponseWriter, r *http.Request) {
		claims, err := verify(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil {
			log.Printf("hello-app %s refused /member: %v", version, err)
			http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
		if !claims.hasRole("member") {
			log.Printf("hello-app %s refused /member to %s: not a member", version, claims.Username)
			http.Error(w, "forbidden: the member role is needed", http.StatusForbidden)
			return
		}
		log.Printf("hello-app %s served /member to %s", version, claims.Username)
		fmt.Fprintf(w, "hello member %s, from urbex %s (%s)\n", claims.Username, version, os.Getenv("GREETING_ENV"))
	})
	// Test only: lets the scenario see that a secret reached the service.
	http.HandleFunc("/secret", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "token=%s\n", os.Getenv("API_TOKEN"))
	})
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	log.Printf("hello-app %s listening on :%s", version, os.Getenv("PORT"))
	log.Fatal(http.ListenAndServe(":"+os.Getenv("PORT"), nil))
}

type claims struct {
	Issuer      string `json:"iss"`
	Expires     int64  `json:"exp"`
	Username    string `json:"preferred_username"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

func (c claims) hasRole(role string) bool {
	for _, r := range c.RealmAccess.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// verify checks an RS256 JWT against the realm's keys, issuer and expiry.
func verify(token string) (claims, error) {
	var c claims
	parts := strings.Split(token, ".")
	if token == "" || len(parts) != 3 {
		return c, errors.New("no bearer token")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodePart(parts[0], &header); err != nil || header.Alg != "RS256" {
		return c, errors.New("not an RS256 token")
	}
	key, err := publicKey(header.Kid)
	if err != nil {
		return c, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return c, errors.New("bad signature encoding")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return c, errors.New("bad signature")
	}
	if err := decodePart(parts[1], &c); err != nil {
		return c, errors.New("bad claims")
	}
	if c.Issuer != os.Getenv("KEYCLOAK_ISSUER") {
		return c, fmt.Errorf("issuer %q is not this realm's", c.Issuer)
	}
	if time.Now().Unix() > c.Expires {
		return c, errors.New("expired token")
	}
	return c, nil
}

func decodePart(part string, v any) error {
	data, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

var (
	keysMu sync.Mutex
	keys   = map[string]*rsa.PublicKey{}
)

// publicKey returns the realm's signing key kid, fetching the realm's
// keys (JWKS) when it isn't known yet.
func publicKey(kid string) (*rsa.PublicKey, error) {
	keysMu.Lock()
	defer keysMu.Unlock()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	resp, err := http.Get(os.Getenv("KEYCLOAK_JWKS_URL"))
	if err != nil {
		return nil, fmt.Errorf("fetching the realm's keys: %w", err)
	}
	defer resp.Body.Close()
	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, fmt.Errorf("reading the realm's keys: %w", err)
	}
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown signing key %q", kid)
}
