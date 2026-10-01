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
	"log"
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

// maxCredentialFileBytes bounds how much the controller will read from the
// credential file, so a corrupt or hostile file cannot exhaust memory.
const maxCredentialFileBytes = 4 << 20

// AuthStore owns the protected credential file. The file is the authority, not
// the in-memory copy: the CLI writes it from a separate process, so every
// read and every write re-reads it first. Without that, a token revoked with
// the CLI would keep working until the controller restarted, and the next
// controller-side write would resurrect it from the stale copy.
type AuthStore struct {
	mu    sync.Mutex
	path  string
	data  credentialFile
	stamp fileStamp
}

func newAuthStore(path string) (*AuthStore, error) {
	store := &AuthStore{path: path, data: credentialFile{Version: 1}}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.reloadLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

// reloadLocked re-reads the credential file when it changed on disk. A read
// error is reported rather than swallowed, so a verification never silently
// falls back to a stale document.
func (a *AuthStore) reloadLocked() error {
	stamp, err := statFileStamp(a.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			a.data = credentialFile{Version: 1}
			a.stamp = fileStamp{}
			return nil
		}
		return err
	}
	if stamp.equal(a.stamp) && a.stamp != (fileStamp{}) {
		return nil
	}
	if stamp.size > maxCredentialFileBytes {
		return fmt.Errorf("credential file %s is %d bytes, which exceeds the %d byte limit", a.path, stamp.size, maxCredentialFileBytes)
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			a.data = credentialFile{Version: 1}
			a.stamp = fileStamp{}
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
	a.stamp = stamp
	return nil
}

// mutate re-reads the file, applies the change, and writes it back, so a
// concurrent CLI edit is never clobbered by a stale in-memory document.
func (a *AuthStore) mutate(fn func(data *credentialFile) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.reloadLocked(); err != nil {
		return err
	}
	next := a.data.clone()
	if err := fn(&next); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomicSync(a.path, encoded, 0o600); err != nil {
		return err
	}
	a.data = next
	a.stamp, _ = statFileStamp(a.path)
	return nil
}

// read re-reads the file and hands the callback the current document.
func (a *AuthStore) read(fn func(data credentialFile)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.reloadLocked(); err != nil {
		return err
	}
	fn(a.data)
	return nil
}

func (d credentialFile) clone() credentialFile {
	next := credentialFile{Version: d.Version}
	if d.Admin != nil {
		admin := *d.Admin
		next.Admin = &admin
	}
	next.APITokens = append([]TokenRecord(nil), d.APITokens...)
	next.AgentTokens = append([]TokenRecord(nil), d.AgentTokens...)
	return next
}

// HasAdmin reports whether local bootstrap has already happened.
func (a *AuthStore) HasAdmin() bool {
	present := false
	if err := a.read(func(data credentialFile) { present = data.Admin != nil }); err != nil {
		return false
	}
	return present
}

// AdminUpdatedAt reports when the admin credential last changed. Sessions
// created before that are no longer trusted, which is how a password rotation
// made with the CLI invalidates browser sessions in a running controller.
func (a *AuthStore) AdminUpdatedAt() time.Time {
	var updated time.Time
	_ = a.read(func(data credentialFile) {
		if data.Admin != nil {
			updated = data.Admin.UpdatedAt
		}
	})
	return updated
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
	return a.mutate(func(data *credentialFile) error {
		data.Admin = &PasswordRecord{
			Username:   username,
			Salt:       hex.EncodeToString(salt),
			Hash:       hex.EncodeToString(hash),
			Iterations: passwordIterations,
			UpdatedAt:  time.Now(),
		}
		return nil
	})
}

// VerifyPassword checks a login attempt in constant time.
func (a *AuthStore) VerifyPassword(username, password string) error {
	var record *PasswordRecord
	if err := a.read(func(data credentialFile) {
		if data.Admin != nil {
			copied := *data.Admin
			record = &copied
		}
	}); err != nil {
		return err
	}
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
	if err := a.mutate(func(data *credentialFile) error {
		if agentID != "" {
			// Rotation replaces the previous token for the same identity.
			kept := make([]TokenRecord, 0, len(data.AgentTokens))
			for _, existing := range data.AgentTokens {
				if existing.AgentID != agentID {
					kept = append(kept, existing)
				}
			}
			data.AgentTokens = append(kept, record)
			return nil
		}
		data.APITokens = append(data.APITokens, record)
		return nil
	}); err != nil {
		return "", TokenRecord{}, err
	}
	return secret, record, nil
}

// RevokeAPIToken removes one automation token by ID.
func (a *AuthStore) RevokeAPIToken(id string) error {
	return a.mutate(func(data *credentialFile) error {
		kept := make([]TokenRecord, 0, len(data.APITokens))
		found := false
		for _, token := range data.APITokens {
			if token.ID == id {
				found = true
				continue
			}
			kept = append(kept, token)
		}
		if !found {
			return errTokenNotFound
		}
		data.APITokens = kept
		return nil
	})
}

// RevokeAgentToken removes the token of one agent identity.
func (a *AuthStore) RevokeAgentToken(agentID string) error {
	return a.mutate(func(data *credentialFile) error {
		kept := make([]TokenRecord, 0, len(data.AgentTokens))
		found := false
		for _, token := range data.AgentTokens {
			if token.AgentID == agentID {
				found = true
				continue
			}
			kept = append(kept, token)
		}
		if !found {
			return errTokenNotFound
		}
		data.AgentTokens = kept
		return nil
	})
}

// VerifyAPIToken matches a bearer token against the automation token list.
func (a *AuthStore) VerifyAPIToken(secret string) (TokenRecord, bool) {
	hash := hashToken(secret)
	var found TokenRecord
	matched := false
	if err := a.read(func(data credentialFile) {
		for _, token := range data.APITokens {
			if subtle.ConstantTimeCompare([]byte(token.Hash), []byte(hash)) == 1 {
				found, matched = token, true
				return
			}
		}
	}); err != nil {
		// A credential file that cannot be read authenticates nobody.
		log.Printf("read credentials: %v", err)
		return TokenRecord{}, false
	}
	return found, matched
}

// VerifyAgentToken resolves a bearer token to exactly one agent identity.
func (a *AuthStore) VerifyAgentToken(secret string) (TokenRecord, bool) {
	hash := hashToken(secret)
	var found TokenRecord
	matched := false
	if err := a.read(func(data credentialFile) {
		for _, token := range data.AgentTokens {
			if subtle.ConstantTimeCompare([]byte(token.Hash), []byte(hash)) == 1 {
				found, matched = token, true
				return
			}
		}
	}); err != nil {
		log.Printf("read credentials: %v", err)
		return TokenRecord{}, false
	}
	return found, matched
}

// APITokens returns token metadata without any verifier material.
func (a *AuthStore) APITokens() []TokenRecord {
	var tokens []TokenRecord
	_ = a.read(func(data credentialFile) { tokens = redactTokens(data.APITokens) })
	return tokens
}

// AgentTokens returns agent token metadata without any verifier material.
func (a *AuthStore) AgentTokens() []TokenRecord {
	var tokens []TokenRecord
	_ = a.read(func(data credentialFile) { tokens = redactTokens(data.AgentTokens) })
	return tokens
}

// HasAgentToken reports whether an agent identity has been enrolled.
func (a *AuthStore) HasAgentToken(agentID string) bool {
	enrolled := false
	_ = a.read(func(data credentialFile) {
		for _, token := range data.AgentTokens {
			if token.AgentID == agentID {
				enrolled = true
				return
			}
		}
	})
	return enrolled
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
