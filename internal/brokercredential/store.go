// Package brokercredential stores native logical-session recovery bearers.
// The files never enter plugin memory or broker WAL payloads.
package brokercredential

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/foobarto/stado/internal/broker"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/workdirpath"
)

const (
	credentialSchema  = 1
	maxCredentialFile = 4096
)

var ErrNotFound = errors.New("broker credential: not found")

// Store owns a private per-user directory. Mode 0600 prevents accidental
// cross-client disclosure and forgery by other UIDs. It is not protection from
// a malicious process already running as the same UID, which can generally
// inspect the live orchestrator that necessarily holds the controller bearer.
type Store struct {
	root string
}

type credentialFile struct {
	Schema       int    `json:"schema"`
	Subject      string `json:"subject"`
	Ticket       string `json:"ticket"`
	ResumeSecret string `json:"resume_secret"`
}

func New(stateDir string) (*Store, error) {
	if stateDir == "" {
		return nil, errors.New("broker credential: state directory required")
	}
	return &Store{root: filepath.Join(stateDir, "broker", "session-credentials")}, nil
}

func (s *Store) Save(credential broker.SessionAdoptionCredential) error {
	if err := validate(credential); err != nil {
		return err
	}
	root, err := s.openRoot(true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	data, err := json.Marshal(credentialFile{
		Schema: credentialSchema, Subject: credential.Subject,
		Ticket: credential.Ticket, ResumeSecret: credential.ResumeSecret,
	})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := workdirpath.NewRootResolver(root).WriteFileAtomicExactMode(fileName(credential.Subject), data, 0o600); err != nil {
		return fmt.Errorf("broker credential: write: %w", err)
	}
	return nil
}

// StageHandoff atomically writes the existing stable recovery bearer under an
// exact child subject before the broker commits a logical-subject handoff. The
// source file remains intact: a crash before commit must leave the source
// authoritative, while a crash after commit must find the child credential.
func (s *Store) StageHandoff(sourceSubject, childSubject string) (broker.SessionAdoptionCredential, error) {
	if sourceSubject == childSubject {
		return broker.SessionAdoptionCredential{}, errors.New("broker credential: handoff child must differ from source")
	}
	source, err := s.Load(sourceSubject)
	if err != nil {
		return broker.SessionAdoptionCredential{}, err
	}
	staged := source
	staged.Subject = childSubject
	existing, err := s.Load(childSubject)
	switch {
	case err == nil:
		if existing.Ticket != staged.Ticket || existing.ResumeSecret != staged.ResumeSecret {
			return broker.SessionAdoptionCredential{}, errors.New("broker credential: child already has a different recovery bearer")
		}
		return existing, nil
	case !errors.Is(err, ErrNotFound):
		return broker.SessionAdoptionCredential{}, err
	}
	if err := s.Save(staged); err != nil {
		return broker.SessionAdoptionCredential{}, err
	}
	return staged, nil
}

func (s *Store) Load(subject string) (broker.SessionAdoptionCredential, error) {
	if err := stadogit.ValidateSessionID(subject); err != nil {
		return broker.SessionAdoptionCredential{}, fmt.Errorf("broker credential: subject: %w", err)
	}
	root, err := s.openRoot(false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return broker.SessionAdoptionCredential{}, ErrNotFound
		}
		return broker.SessionAdoptionCredential{}, err
	}
	defer func() { _ = root.Close() }()
	name := fileName(subject)
	info, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return broker.SessionAdoptionCredential{}, ErrNotFound
		}
		return broker.SessionAdoptionCredential{}, fmt.Errorf("broker credential: stat: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return broker.SessionAdoptionCredential{}, fmt.Errorf("broker credential: %s must be a regular 0600 file", name)
	}
	data, err := workdirpath.NewRootResolver(root).ReadFileLimited(name, maxCredentialFile)
	if err != nil {
		return broker.SessionAdoptionCredential{}, fmt.Errorf("broker credential: read: %w", err)
	}
	var stored credentialFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return broker.SessionAdoptionCredential{}, fmt.Errorf("broker credential: decode: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return broker.SessionAdoptionCredential{}, err
	}
	credential := broker.SessionAdoptionCredential{
		Subject: stored.Subject, Ticket: stored.Ticket, ResumeSecret: stored.ResumeSecret,
	}
	if stored.Schema != credentialSchema || stored.Subject != subject {
		return broker.SessionAdoptionCredential{}, errors.New("broker credential: schema or subject mismatch")
	}
	if err := validate(credential); err != nil {
		return broker.SessionAdoptionCredential{}, err
	}
	return credential, nil
}

func (s *Store) Remove(subject string) error {
	if err := stadogit.ValidateSessionID(subject); err != nil {
		return fmt.Errorf("broker credential: subject: %w", err)
	}
	path := filepath.Join(s.root, fileName(subject))
	if err := workdirpath.NewUserConfigResolver().RemoveAll(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("broker credential: remove: %w", err)
	}
	return nil
}

func (s *Store) openRoot(create bool) (*os.Root, error) {
	resolver := workdirpath.NewUserConfigResolver()
	if create {
		if err := resolver.MkdirAll(s.root, 0o700); err != nil {
			return nil, fmt.Errorf("broker credential: create private root: %w", err)
		}
	}
	root, err := resolver.OpenRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("broker credential: open private root: %w", err)
	}
	info, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("broker credential: stat private root: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		_ = root.Close()
		return nil, errors.New("broker credential: private root must be a 0700 directory")
	}
	return root, nil
}

func validate(credential broker.SessionAdoptionCredential) error {
	if err := stadogit.ValidateSessionID(credential.Subject); err != nil {
		return fmt.Errorf("broker credential: subject: %w", err)
	}
	if len(credential.Ticket) != len("scope_")+64 || !strings.HasPrefix(credential.Ticket, "scope_") ||
		len(credential.ResumeSecret) != len("resume_")+64 || !strings.HasPrefix(credential.ResumeSecret, "resume_") {
		return errors.New("broker credential: malformed bearer")
	}
	if _, err := hex.DecodeString(credential.Ticket[len("scope_"):]); err != nil {
		return errors.New("broker credential: malformed ticket")
	}
	if _, err := hex.DecodeString(credential.ResumeSecret[len("resume_"):]); err != nil {
		return errors.New("broker credential: malformed resume secret")
	}
	return nil
}

func fileName(subject string) string {
	digest := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(digest[:]) + ".json"
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("broker credential: trailing JSON value")
		}
		return fmt.Errorf("broker credential: trailing data: %w", err)
	}
	return nil
}
