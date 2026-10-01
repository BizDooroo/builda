package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Credential handling. The credential file is separate from the controller
// config, is created with mode 0600, and never leaves the controller: the API
// returns raw secrets exactly once at creation and only stores verifiers.
const (
	passwordIterations = 210000
	passwordKeyLength  = 32
	tokenBytes         = 32
	adminUserName      = "admin"
)

var (
	errNoAdminCredential  = errors.New("no admin credential is configured; run \"builda controller admin set-password\" on the controller host first")
	errInvalidCredentials = errors.New("invalid credentials")
	errTokenNotFound      = errors.New("token not found")
)

// PasswordRecord stores a salted PBKDF2-HMAC-SHA256 verifier.
type PasswordRecord struct {
	Username   string    `json:"username"`
	Salt       string    `json:"salt"`
	Hash       string    `json:"hash"`
	Iterations int       `json:"iterations"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TokenRecord stores only the SHA-256 verifier of a high-entropy token.
type TokenRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	AgentID   string    `json:"agent_id,omitempty"`
	Hash      string    `json:"hash,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
}

type credentialFile struct {
	Version     int             `json:"version"`
	Admin       *PasswordRecord `json:"admin,omitempty"`
	APITokens   []TokenRecord   `json:"api_tokens,omitempty"`
	AgentTokens []TokenRecord   `json:"agent_tokens,omitempty"`
}

// AuthStore owns the protected credential file.
type AuthStore struct {
	mu   sync.Mutex
	path string
	data credentialFile
}

func newAuthStore(path string) (*AuthStore, error) {
	store := &AuthStore{path: path, data: credentialFile{Version: 1}}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (a *AuthStore) load() error {
	data, err := os.ReadFile(a.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var parsed credentialFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("parse credential file %s: %w", a.path, err)
	}
	if parsed.Version == 0 {
		parsed.Version = 1
	}
	a.data = parsed
	return nil
}

func (a *AuthStore) saveLocked() error {
	encoded, err := json.MarshalIndent(a.data, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomicSync(a.path, encoded, 0o600)
}

// HasAdmin reports whether local bootstrap has already happened.
func (a *AuthStore) HasAdmin() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.data.Admin != nil
}

// SetAdminPassword creates or rotates the single admin credential.
func (a *AuthStore) SetAdminPassword(username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		username = adminUserName
	}
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash, err := derivePassword(password, salt, passwordIterations)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.data.Admin = &PasswordRecord{
		Username:   username,
		Salt:       hex.EncodeToString(salt),
		Hash:       hex.EncodeToString(hash),
		Iterations: passwordIterations,
		UpdatedAt:  time.Now(),
	}
	return a.saveLocked()
}

// VerifyPassword checks a login attempt in constant time.
func (a *AuthStore) VerifyPassword(username, password string) error {
	a.mu.Lock()
	record := a.data.Admin
	a.mu.Unlock()
	if record == nil {
		return errNoAdminCredential
	}
	salt, err := hex.DecodeString(record.Salt)
	if err != nil {
		return errInvalidCredentials
	}
	hash, err := derivePassword(password, salt, record.Iterations)
	if err != nil {
		return errInvalidCredentials
	}
	expected, err := hex.DecodeString(record.Hash)
	if err != nil {
		return errInvalidCredentials
	}
	userMatch := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(username)), []byte(record.Username))
	hashMatch := subtle.ConstantTimeCompare(hash, expected)
	if userMatch != 1 || hashMatch != 1 {
		return errInvalidCredentials
	}
	return nil
}

func derivePassword(password string, salt []byte, iterations int) ([]byte, error) {
	if iterations <= 0 {
		iterations = passwordIterations
	}
	return pbkdf2.Key(sha256.New, password, salt, iterations, passwordKeyLength)
}

// IssueAPIToken creates an automation token and returns the raw secret once.
func (a *AuthStore) IssueAPIToken(name string) (string, TokenRecord, error) {
	return a.issueToken(name, "")
}

// IssueAgentToken creates or rotates the token of one agent identity.
func (a *AuthStore) IssueAgentToken(agentID string) (string, TokenRecord, error) {
	if !validIdentifier(agentID) {
		return "", TokenRecord{}, fmt.Errorf("invalid agent id %q", agentID)
	}
	return a.issueToken("", agentID)
}

func (a *AuthStore) issueToken(name, agentID string) (string, TokenRecord, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", TokenRecord{}, err
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	record := TokenRecord{
		ID:        randomHex(8),
		Name:      strings.TrimSpace(name),
		AgentID:   agentID,
		Hash:      hashToken(secret),
		CreatedAt: time.Now(),
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if agentID != "" {
		// Rotation replaces the previous token for the same agent identity.
		kept := a.data.AgentTokens[:0]
		for _, existing := range a.data.AgentTokens {
			if existing.AgentID != agentID {
				kept = append(kept, existing)
			}
		}
		a.data.AgentTokens = append(kept, record)
	} else {
		a.data.APITokens = append(a.data.APITokens, record)
	}
	if err := a.saveLocked(); err != nil {
		return "", TokenRecord{}, err
	}
	return secret, record, nil
}

// RevokeAPIToken removes one automation token by ID.
func (a *AuthStore) RevokeAPIToken(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	kept := make([]TokenRecord, 0, len(a.data.APITokens))
	found := false
	for _, token := range a.data.APITokens {
		if token.ID == id {
			found = true
			continue
		}
		kept = append(kept, token)
	}
	if !found {
		return errTokenNotFound
	}
	a.data.APITokens = kept
	return a.saveLocked()
}

// RevokeAgentToken removes the token of one agent identity.
func (a *AuthStore) RevokeAgentToken(agentID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	kept := make([]TokenRecord, 0, len(a.data.AgentTokens))
	found := false
	for _, token := range a.data.AgentTokens {
		if token.AgentID == agentID {
			found = true
			continue
		}
		kept = append(kept, token)
	}
	if !found {
		return errTokenNotFound
	}
	a.data.AgentTokens = kept
	return a.saveLocked()
}

// VerifyAPIToken matches a bearer token against the automation token list.
func (a *AuthStore) VerifyAPIToken(secret string) (TokenRecord, bool) {
	hash := hashToken(secret)
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, token := range a.data.APITokens {
		if subtle.ConstantTimeCompare([]byte(token.Hash), []byte(hash)) == 1 {
			return token, true
		}
	}
	return TokenRecord{}, false
}

// VerifyAgentToken resolves a bearer token to exactly one agent identity.
func (a *AuthStore) VerifyAgentToken(secret string) (TokenRecord, bool) {
	hash := hashToken(secret)
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, token := range a.data.AgentTokens {
		if subtle.ConstantTimeCompare([]byte(token.Hash), []byte(hash)) == 1 {
			return token, true
		}
	}
	return TokenRecord{}, false
}

// APITokens returns token metadata without any verifier material.
func (a *AuthStore) APITokens() []TokenRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	return redactTokens(a.data.APITokens)
}

// AgentTokens returns agent token metadata without any verifier material.
func (a *AuthStore) AgentTokens() []TokenRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	return redactTokens(a.data.AgentTokens)
}

// HasAgentToken reports whether an agent identity has been enrolled.
func (a *AuthStore) HasAgentToken(agentID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, token := range a.data.AgentTokens {
		if token.AgentID == agentID {
			return true
		}
	}
	return false
}

func redactTokens(tokens []TokenRecord) []TokenRecord {
	redacted := make([]TokenRecord, 0, len(tokens))
	for _, token := range tokens {
		token.Hash = ""
		redacted = append(redacted, token)
	}
	return redacted
}

func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
