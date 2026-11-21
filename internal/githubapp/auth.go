package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v93/github"
)

const defaultGitHubAPIBaseURL = "https://api.github.com"

// InstallationAuth mints RS256 GitHub App JWTs using Go stdlib crypto/rsa,
// caches 1-hour installation access tokens, and acts as an http.RoundTripper.
type InstallationAuth struct {
	appID          int64
	installationID int64
	privateKey     *rsa.PrivateKey
	apiBaseURL     string
	baseTransport  http.RoundTripper
	httpClient     *http.Client
	nowFn          func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// NewInstallationAuth constructs a stdlib GitHub App token provider and HTTP transport.
// pemOrBase64 may be either a Base64-encoded PEM string (as stored in Keychain) or a raw PEM block.
func NewInstallationAuth(
	appID int64,
	installationID int64,
	pemOrBase64 string,
	apiBaseURL string,
	baseTransport http.RoundTripper,
) (*InstallationAuth, error) {
	if appID <= 0 {
		return nil, errors.New("github app ID must be positive")
	}
	if installationID <= 0 {
		return nil, errors.New("github installation ID must be positive")
	}
	key, err := parseRSAPrivateKey(pemOrBase64)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiBaseURL) == "" {
		apiBaseURL = defaultGitHubAPIBaseURL
	}
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	return &InstallationAuth{
		appID:          appID,
		installationID: installationID,
		privateKey:     key,
		apiBaseURL:     strings.TrimRight(apiBaseURL, "/"),
		baseTransport:  baseTransport,
		httpClient: &http.Client{
			Transport: baseTransport,
			Timeout:   30 * time.Second,
		},
		nowFn: time.Now,
	}, nil
}

// NewGitHubClient wraps go-github/v93 with the stdlib InstallationAuth RoundTripper.
func NewGitHubClient(auth *InstallationAuth) (*github.Client, error) {
	if auth == nil {
		return nil, errors.New("installation auth is nil")
	}
	opts := []github.ClientOptionsFunc{
		github.WithHTTPClient(&http.Client{
			Transport: auth,
			Timeout:   45 * time.Second,
		}),
	}
	if auth.apiBaseURL != "" && auth.apiBaseURL != defaultGitHubAPIBaseURL {
		baseURL := strings.TrimRight(auth.apiBaseURL, "/") + "/"
		opts = append(opts, github.WithURLs(&baseURL, &baseURL))
	}
	return github.NewClient(opts...)
}

// SignAppJWT produces a short-lived RS256 GitHub App JWT.
func (a *InstallationAuth) SignAppJWT(now time.Time) (string, error) {
	headerJSON, err := json.Marshal(struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}{
		Alg: "RS256",
		Typ: "JWT",
	})
	if err != nil {
		return "", fmt.Errorf("marshal jwt header: %w", err)
	}

	claimsJSON, err := json.Marshal(struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}{
		Iat: now.Add(-60 * time.Second).Unix(),
		Exp: now.Add(9 * time.Minute).Unix(),
		Iss: strconv.FormatInt(a.appID, 10),
	})
	if err != nil {
		return "", fmt.Errorf("marshal jwt claims: %w", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Token returns a valid cached installation access token or exchanges a new one via GitHub API.
func (a *InstallationAuth) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.nowFn()
	if a.token != "" && a.expiresAt.After(now.Add(2*time.Minute)) {
		return a.token, nil
	}

	jwtToken, err := a.SignAppJWT(now)
	if err != nil {
		return "", err
	}

	endpoint := fmt.Sprintf("%s/app/installations/%d/access_tokens", a.apiBaseURL, a.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(nil))
	if err != nil {
		return "", fmt.Errorf("create access token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ai-factory")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("exchange installation token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read installation token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("github installation token HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode installation token response: %w", err)
	}
	if parsed.Token == "" {
		return "", errors.New("github returned an empty installation token")
	}
	if parsed.ExpiresAt.IsZero() {
		parsed.ExpiresAt = now.Add(55 * time.Minute)
	}

	a.token = parsed.Token
	a.expiresAt = parsed.ExpiresAt
	return a.token, nil
}

// RoundTrip injects a fresh GitHub App installation token onto outgoing API requests.
func (a *InstallationAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := a.Token(req.Context())
	if err != nil {
		return nil, err
	}

	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "token "+token)
	if clone.Header.Get("Accept") == "" {
		clone.Header.Set("Accept", "application/vnd.github+json")
	}
	if clone.Header.Get("X-GitHub-Api-Version") == "" {
		clone.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	if clone.Header.Get("User-Agent") == "" {
		clone.Header.Set("User-Agent", "ai-factory")
	}
	return a.baseTransport.RoundTrip(clone)
}

func parseRSAPrivateKey(pemOrBase64 string) (*rsa.PrivateKey, error) {
	trimmed := strings.TrimSpace(pemOrBase64)
	if trimmed == "" {
		return nil, errors.New("github app private key is empty")
	}

	raw := []byte(trimmed)
	if !strings.Contains(trimmed, "-----BEGIN") {
		decoded, err := base64.StdEncoding.DecodeString(trimmed)
		if err != nil {
			return nil, fmt.Errorf("decode base64 private key: %w", err)
		}
		raw = decoded
	}

	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("failed to decode PEM private key block")
	}

	if pkcs1, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return pkcs1, nil
	}
	pkcs8, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse RSA private key (PKCS#1 and PKCS#8 failed): %w", err)
	}
	rsaKey, ok := pkcs8.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github app private key must be an RSA key")
	}
	return rsaKey, nil
}
