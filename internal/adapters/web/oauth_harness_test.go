package web_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/ory/fosite"
)

// memOAuthStore est un magasin OAuth en mémoire, écrit pour la suite de l'adapter
// web.
//
// Il reproduit **délibérément** la sémantique de l'implémentation PostgreSQL, y
// compris ses détails les moins évidents : l'invalidation qui conserve
// l'enregistrement, le couple (requête, erreur) rendu pour un code ou un jeton
// désactivé, la révocation par identifiant de requête. Sans cette fidélité, les
// tests de flux vérifieraient un protocole qui ne tourne nulle part.
//
// Ce qui reste différent est assumé et vérifié ailleurs : le SQL réel a ses
// propres tests d'intégration, dans adapters/postgres, contre un vrai PostgreSQL.
type memOAuthStore struct {
	mu      sync.Mutex
	clients map[string]*memOAuthClient
	// records est indexé par nature puis par signature, comme la clé primaire
	// composite de la table oauth_tokens.
	records map[string]map[string]*memOAuthRecord
	// grants tient la mémoire des consentements, indexée par « compte|client »
	// comme la clé primaire composite de la table oauth_grants.
	grants map[string]time.Time
}

// Natures d'enregistrement, identiques à celles de la table.
const (
	memKindAuthorizationCode = "authorization_code"
	memKindAccessToken       = "access_token"
	memKindRefreshToken      = "refresh_token"
	memKindPKCE              = "pkce"
)

// memOAuthRecord est une requête OAuth gelée. Les champs sont ceux que la table
// porte, sérialisés de la même façon : ce qui circule ici a fait le même
// aller-retour que ce qui circulerait par la base.
type memOAuthRecord struct {
	requestID     string
	clientID      string
	requestedAt   time.Time
	requested     []string
	granted       []string
	requestedAud  []string
	grantedAud    []string
	form          []byte
	session       []byte
	active        bool
	accessSigning string
}

// memOAuthClient est un client enregistré. Il porte GetName et GetRegisteredAt,
// les extensions facultatives que la page de consentement reconnaît par
// assertion de type.
type memOAuthClient struct {
	fosite.DefaultClient

	name         string
	registeredAt time.Time
}

func (c *memOAuthClient) GetName() string {
	return c.name
}

func (c *memOAuthClient) GetRegisteredAt() time.Time {
	return c.registeredAt
}

func newMemOAuthStore() *memOAuthStore {
	return &memOAuthStore{
		clients: make(map[string]*memOAuthClient),
		records: map[string]map[string]*memOAuthRecord{
			memKindAuthorizationCode: {},
			memKindAccessToken:       {},
			memKindRefreshToken:      {},
			memKindPKCE:              {},
		},
		grants: make(map[string]time.Time),
	}
}

// --- Clients ----------------------------------------------------------------

func (s *memOAuthStore) GetClient(_ context.Context, id string) (fosite.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return nil, fosite.ErrNotFound
	}

	return client, nil
}

func (s *memOAuthStore) CreateClient(_ context.Context, client *fosite.DefaultClient, name string, registeredAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.clients[client.ID]; exists {
		return fmt.Errorf("client OAuth déjà enregistré : %s", client.ID)
	}

	stored := *client
	s.clients[client.ID] = &memOAuthClient{DefaultClient: stored, name: name, registeredAt: registeredAt}

	return nil
}

// --- Consentements ----------------------------------------------------------

// grantKey réunit compte et client, comme la clé primaire de la table
// oauth_grants.
func grantKey(userID, clientID string) string {
	return userID + "|" + clientID
}

func (s *memOAuthStore) HasGrant(_ context.Context, userID, clientID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, granted := s.grants[grantKey(userID, clientID)]

	return granted, nil
}

// RecordGrant reproduit l'idempotence du SQL réel : la première date est
// conservée, un second consentement ne la réécrit pas.
func (s *memOAuthStore) RecordGrant(_ context.Context, userID, clientID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := grantKey(userID, clientID)
	if _, exists := s.grants[key]; !exists {
		s.grants[key] = at
	}

	return nil
}

func (s *memOAuthStore) CountClients(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.clients), nil
}

func (s *memOAuthStore) ClientAssertionJWTValid(context.Context, string) error {
	return fosite.ErrNotFound
}

func (s *memOAuthStore) SetClientAssertionJWT(context.Context, string, time.Time) error {
	return fosite.ErrNotFound
}

// --- Codes d'autorisation ---------------------------------------------------

func (s *memOAuthStore) CreateAuthorizeCodeSession(_ context.Context, code string, request fosite.Requester) error {
	return s.insert(memKindAuthorizationCode, code, request, "")
}

func (s *memOAuthStore) GetAuthorizeCodeSession(_ context.Context, code string, session fosite.Session) (fosite.Requester, error) {
	request, active, err := s.fetch(memKindAuthorizationCode, code, session)
	if err != nil {
		return nil, err
	}
	if !active {
		return request, fosite.ErrInvalidatedAuthorizeCode
	}
	return request, nil
}

func (s *memOAuthStore) InvalidateAuthorizeCodeSession(_ context.Context, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.records[memKindAuthorizationCode][code]
	if !ok {
		return fosite.ErrNotFound
	}
	record.active = false

	return nil
}

// --- Jetons d'accès ---------------------------------------------------------

func (s *memOAuthStore) CreateAccessTokenSession(_ context.Context, signature string, request fosite.Requester) error {
	return s.insert(memKindAccessToken, signature, request, "")
}

func (s *memOAuthStore) GetAccessTokenSession(_ context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	return s.fetchActive(memKindAccessToken, signature, session)
}

func (s *memOAuthStore) DeleteAccessTokenSession(_ context.Context, signature string) error {
	return s.remove(memKindAccessToken, signature)
}

// --- Jetons de rafraîchissement ---------------------------------------------

func (s *memOAuthStore) CreateRefreshTokenSession(_ context.Context, signature, accessSignature string, request fosite.Requester) error {
	return s.insert(memKindRefreshToken, signature, request, accessSignature)
}

func (s *memOAuthStore) GetRefreshTokenSession(_ context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	request, active, err := s.fetch(memKindRefreshToken, signature, session)
	if err != nil {
		return nil, err
	}
	if !active {
		return request, fosite.ErrInactiveToken
	}
	return request, nil
}

func (s *memOAuthStore) DeleteRefreshTokenSession(_ context.Context, signature string) error {
	return s.remove(memKindRefreshToken, signature)
}

// RotateRefreshToken reproduit la rotation du magasin SQL, y compris son refus
// de faire tourner deux fois le même jeton : c'est ce refus qui, en base, tranche
// la course entre deux rafraîchissements simultanés.
func (s *memOAuthStore) RotateRefreshToken(ctx context.Context, requestID, signature string) error {
	if err := s.rotatePresented(signature); err != nil {
		return err
	}
	if err := s.RevokeRefreshToken(ctx, requestID); err != nil {
		return err
	}
	return s.RevokeAccessToken(ctx, requestID)
}

func (s *memOAuthStore) rotatePresented(signature string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.records[memKindRefreshToken][signature]
	if !ok || !record.active {
		return fosite.ErrSerializationFailure
	}
	record.active = false

	return nil
}

// --- Révocation -------------------------------------------------------------

func (s *memOAuthStore) RevokeRefreshToken(_ context.Context, requestID string) error {
	s.deactivateFamily(memKindRefreshToken, requestID)
	return nil
}

func (s *memOAuthStore) RevokeAccessToken(_ context.Context, requestID string) error {
	s.deactivateFamily(memKindAccessToken, requestID)
	return nil
}

// --- PKCE -------------------------------------------------------------------

func (s *memOAuthStore) CreatePKCERequestSession(_ context.Context, signature string, request fosite.Requester) error {
	return s.insert(memKindPKCE, signature, request, "")
}

func (s *memOAuthStore) GetPKCERequestSession(_ context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	return s.fetchActive(memKindPKCE, signature, session)
}

func (s *memOAuthStore) DeletePKCERequestSession(_ context.Context, signature string) error {
	return s.remove(memKindPKCE, signature)
}

// --- Mécanique commune ------------------------------------------------------

func (s *memOAuthStore) insert(kind, signature string, request fosite.Requester, accessSignature string) error {
	form, err := json.Marshal(request.GetRequestForm())
	if err != nil {
		return err
	}
	session, err := json.Marshal(request.GetSession())
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.records[kind][signature] = &memOAuthRecord{
		requestID:     request.GetID(),
		clientID:      request.GetClient().GetID(),
		requestedAt:   request.GetRequestedAt(),
		requested:     request.GetRequestedScopes(),
		granted:       request.GetGrantedScopes(),
		requestedAud:  request.GetRequestedAudience(),
		grantedAud:    request.GetGrantedAudience(),
		form:          form,
		session:       session,
		active:        true,
		accessSigning: accessSignature,
	}

	return nil
}

func (s *memOAuthStore) fetchActive(kind, signature string, session fosite.Session) (fosite.Requester, error) {
	request, active, err := s.fetch(kind, signature, session)
	if err != nil {
		return nil, err
	}
	if !active {
		return request, fosite.ErrInactiveToken
	}
	return request, nil
}

func (s *memOAuthStore) fetch(kind, signature string, session fosite.Session) (*fosite.Request, bool, error) {
	if session == nil {
		session = new(fosite.DefaultSession)
	}

	s.mu.Lock()
	record, ok := s.records[kind][signature]
	var client *memOAuthClient
	if ok {
		client = s.clients[record.clientID]
	}
	s.mu.Unlock()

	if !ok || client == nil {
		return nil, false, fosite.ErrNotFound
	}

	if err := json.Unmarshal(record.session, session); err != nil {
		return nil, false, err
	}
	form := url.Values{}
	if err := json.Unmarshal(record.form, &form); err != nil {
		return nil, false, err
	}

	return &fosite.Request{
		ID:                record.requestID,
		RequestedAt:       record.requestedAt,
		Client:            client,
		RequestedScope:    record.requested,
		GrantedScope:      record.granted,
		RequestedAudience: record.requestedAud,
		GrantedAudience:   record.grantedAud,
		Form:              form,
		Session:           session,
	}, record.active, nil
}

func (s *memOAuthStore) remove(kind, signature string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.records[kind], signature)

	return nil
}

func (s *memOAuthStore) deactivateFamily(kind, requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, record := range s.records[kind] {
		if record.requestID == requestID {
			record.active = false
		}
	}
}

// expire recule la date d'expiration de tous les enregistrements d'une nature.
//
// C'est le seul moyen de vérifier le contrôle d'expiration : fosite lit
// l'horloge du système, qu'un test ne peut pas avancer. Présenter un
// enregistrement déjà périmé revient au même du point de vue du code testé.
func (s *memOAuthStore) expire(t *testing.T, kind string, tokenType fosite.TokenType, at time.Time) {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, record := range s.records[kind] {
		var session fosite.DefaultSession
		if err := json.Unmarshal(record.session, &session); err != nil {
			t.Fatalf("session illisible : %v", err)
		}

		session.SetExpiresAt(tokenType, at)

		updated, err := json.Marshal(&session)
		if err != nil {
			t.Fatalf("session non sérialisable : %v", err)
		}
		record.session = updated
	}
}

// countActive compte les enregistrements encore valides d'une nature. Les tests
// s'en servent pour vérifier ce que la rotation et la révocation ont réellement
// retiré de la circulation.
func (s *memOAuthStore) countActive(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	total := 0
	for _, record := range s.records[kind] {
		if record.active {
			total++
		}
	}

	return total
}
