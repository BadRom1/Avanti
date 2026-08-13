package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ory/fosite"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// Garde-fous de l'enregistrement dynamique.
//
// Le point de terminaison est ouvert : c'est le modèle que MCP impose, parce
// qu'un agent qui découvre un serveur n'a par définition pas de compte pour s'y
// annoncer. Ouvert ne veut pas dire sans limite, et ces quatre bornes sont ce
// qui sépare « n'importe qui peut enregistrer un client » de « n'importe qui
// peut remplir la base ».
const (
	// maxRegisteredClients plafonne la table. Une instance d'Avanti sert deux
	// personnes et une poignée d'agents ; cinquante est large de deux ordres de
	// grandeur, et fait quand même échouer une boucle d'enregistrement en
	// quelques secondes plutôt qu'en quelques jours.
	maxRegisteredClients = 50
	// maxClientNameLength borne le nom affiché sur la page de consentement. Un
	// nom démesuré ne serait pas une faille — html/template l'échappe — mais il
	// noierait les scopes que l'utilisateur doit lire avant de décider.
	maxClientNameLength = 120
	// maxRedirectURIs borne le nombre d'adresses de retour. Un client légitime en
	// déclare une, parfois deux.
	maxRedirectURIs = 5
	// maxRegistrationBody borne la taille du corps accepté, avant même de
	// l'analyser.
	maxRegistrationBody = 8 << 10
)

// Réglages de la limite de débit sur l'enregistrement.
const (
	// registrationsPerWindow est le nombre d'enregistrements tolérés depuis une
	// même adresse pendant registrationWindow.
	registrationsPerWindow = 5
	registrationWindow     = time.Hour
	// registrationTrackedCap borne le nombre d'adresses suivies, pour la même
	// raison que trackedCap du garde-fou de connexion : un compteur qui grandit
	// sans fin devient lui-même la faille.
	registrationTrackedCap = 1024
)

// defaultClientName nomme un client qui ne s'est pas présenté. Il apparaît tel
// quel sur la page de consentement : mieux vaut « Client sans nom » qu'une ligne
// vide, qui laisserait croire à un défaut d'affichage.
const defaultClientName = "Client sans nom"

// registrationRequest est le corps de la demande, tel que la RFC 7591 le décrit.
//
// Seuls les champs qu'Avanti exploite y figurent. Les autres métadonnées de la
// RFC — logo_uri, contacts, tos_uri… — sont ignorées : les stocker sans les
// afficher n'apporterait rien, et les afficher exposerait la page de
// consentement à des contenus qu'un inconnu choisit.
type registrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// registrationResponse est la réponse de la RFC 7591 §3.2.1.
//
// Aucun client_secret : les clients d'Avanti sont publics. Aucun
// registration_access_token non plus, puisque la gestion du client après coup
// (RFC 7592) n'est pas proposée — un client dont on veut se défaire se révoque
// en base, pas par une API ouverte.
type registrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// registrationError est le format d'erreur de la RFC 7591 §3.2.2.
type registrationError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// handleOAuthRegister traite l'enregistrement dynamique d'un client.
func (h *Handler) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	setOAuthCORS(w.Header())

	if !h.oauth.limiter.allow(callerAddr(r)) {
		h.writeRegistrationError(w, r, http.StatusTooManyRequests, "temporarily_unavailable",
			"Trop d'enregistrements depuis cette adresse. Réessayez plus tard.")
		return
	}

	request, err := decodeRegistration(r)
	if err != nil {
		h.writeRegistrationError(w, r, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}

	client, name, err := h.buildClient(request)
	if err != nil {
		var uriErr *redirectURIError
		if errors.As(err, &uriErr) {
			h.writeRegistrationError(w, r, http.StatusBadRequest, "invalid_redirect_uri", uriErr.Error())
			return
		}
		h.writeRegistrationError(w, r, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	}

	// Le plafond est relu juste avant l'écriture. Deux demandes simultanées
	// peuvent encore passer toutes les deux : c'est sans conséquence, le but est
	// de borner une boucle, pas de compter au client près.
	total, err := h.oauth.storage.CountClients(r.Context())
	if err != nil {
		h.fail(r, fmt.Errorf("comptage des clients OAuth : %w", err))
		h.writeRegistrationError(w, r, http.StatusInternalServerError, "server_error",
			"Enregistrement momentanément impossible.")
		return
	}
	if total >= maxRegisteredClients {
		h.writeRegistrationError(w, r, http.StatusForbidden, "access_denied",
			"Cette instance a atteint son plafond de clients enregistrés.")
		return
	}

	issuedAt := h.oauth.clock().UTC()
	if err := h.oauth.storage.CreateClient(r.Context(), client, name, issuedAt); err != nil {
		h.fail(r, fmt.Errorf("enregistrement du client OAuth : %w", err))
		h.writeRegistrationError(w, r, http.StatusInternalServerError, "server_error",
			"Enregistrement momentanément impossible.")
		return
	}

	h.writeJSON(w, r, http.StatusCreated, registrationResponse{
		ClientID:                client.ID,
		ClientIDIssuedAt:        issuedAt.Unix(),
		ClientName:              name,
		RedirectURIs:            client.RedirectURIs,
		GrantTypes:              client.GrantTypes,
		ResponseTypes:           client.ResponseTypes,
		Scope:                   strings.Join(client.Scopes, " "),
		TokenEndpointAuthMethod: "none",
	})
}

// decodeRegistration lit le corps de la demande.
func decodeRegistration(r *http.Request) (registrationRequest, error) {
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		media, _, err := mime.ParseMediaType(contentType)
		if err != nil || media != "application/json" {
			return registrationRequest{}, errors.New("corps attendu en application/json")
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRegistrationBody+1))
	if err != nil {
		return registrationRequest{}, errors.New("corps de la demande illisible")
	}
	if len(body) > maxRegistrationBody {
		return registrationRequest{}, errors.New("corps de la demande trop volumineux")
	}

	var request registrationRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return registrationRequest{}, errors.New("corps de la demande : JSON invalide")
	}

	return request, nil
}

// buildClient valide les métadonnées et fabrique le client à enregistrer.
func (h *Handler) buildClient(request registrationRequest) (*fosite.DefaultClient, string, error) {
	if method := request.TokenEndpointAuthMethod; method != "" && method != "none" {
		return nil, "", fmt.Errorf("token_endpoint_auth_method %q refusé : cette instance n'enregistre que des clients publics", method)
	}

	redirects, err := validateRedirectURIs(request.RedirectURIs)
	if err != nil {
		return nil, "", err
	}

	grants, err := validateGrantTypes(request.GrantTypes)
	if err != nil {
		return nil, "", err
	}

	if errTypes := validateResponseTypes(request.ResponseTypes); errTypes != nil {
		return nil, "", errTypes
	}

	scopes, err := validateRegisteredScopes(request.Scope)
	if err != nil {
		return nil, "", err
	}

	id, err := newClientID()
	if err != nil {
		return nil, "", errors.New("enregistrement momentanément impossible")
	}

	return &fosite.DefaultClient{
		ID:            id,
		RedirectURIs:  redirects,
		GrantTypes:    grants,
		ResponseTypes: []string{"code"},
		Scopes:        scopes,
		// Aucune audience : Avanti n'émet pas de jeton destiné à un tiers.
		Audience: []string{},
		// Public, donc sans secret. C'est ce qui rend PKCE indispensable, et c'est
		// ce que le document de métadonnées annonce.
		Public: true,
	}, clientName(request.ClientName), nil
}

// clientName normalise le nom déclaré.
func clientName(raw string) string {
	name := strings.Join(strings.Fields(raw), " ")
	if name == "" {
		return defaultClientName
	}

	runes := []rune(name)
	if len(runes) > maxClientNameLength {
		return string(runes[:maxClientNameLength])
	}

	return name
}

// redirectURIError distingue le refus d'une adresse de retour des autres refus,
// parce que la RFC 7591 lui réserve son propre code d'erreur.
type redirectURIError struct {
	reason string
}

func (e *redirectURIError) Error() string {
	return e.reason
}

// validateRedirectURIs contrôle les adresses de retour.
//
// C'est le contrôle le plus important de l'enregistrement, et de loin. L'adresse
// de retour est l'endroit où le code d'autorisation est livré : une validation
// laxiste ici transforme le serveur en distributeur de codes pour l'attaquant
// qui a su enregistrer la bonne adresse.
func validateRedirectURIs(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, &redirectURIError{"redirect_uris est obligatoire et ne peut pas être vide"}
	}
	if len(raw) > maxRedirectURIs {
		return nil, &redirectURIError{fmt.Sprintf("redirect_uris : %d adresses, %d au maximum", len(raw), maxRedirectURIs)}
	}

	validated := make([]string, 0, len(raw))
	for _, candidate := range raw {
		if err := validateRedirectURI(candidate); err != nil {
			return nil, err
		}
		validated = append(validated, candidate)
	}

	return validated, nil
}

// validateRedirectURI contrôle une adresse de retour.
func validateRedirectURI(raw string) error {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return &redirectURIError{"redirect_uri vide ou entourée d'espaces"}
	}
	// Un joker rendrait la comparaison exacte de fosite inopérante — c'est le
	// défaut qui a valu à OAuth ses détournements les plus connus.
	if strings.Contains(raw, "*") {
		return &redirectURIError{fmt.Sprintf("redirect_uri %q : les jokers sont refusés", raw)}
	}
	// Un fragment est interdit par la RFC 6749 §3.1.2, et sert surtout à masquer
	// la vraie cible à qui lit l'adresse.
	if strings.Contains(raw, "#") {
		return &redirectURIError{fmt.Sprintf("redirect_uri %q : le fragment est interdit", raw)}
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return &redirectURIError{fmt.Sprintf("redirect_uri %q illisible", raw)}
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return &redirectURIError{fmt.Sprintf("redirect_uri %q : URL absolue attendue", raw)}
	}
	// Les identifiants dans l'URL n'ont aucune raison d'être là et brouillent la
	// lecture de l'hôte réel — « https://confiance.example@attaquant.example ».
	if parsed.User != nil {
		return &redirectURIError{fmt.Sprintf("redirect_uri %q : identifiants dans l'URL refusés", raw)}
	}

	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		// http n'est toléré que sur la boucle locale, où il n'y a pas de réseau à
		// écouter. C'est ce qui permet à un agent installé sur le poste de
		// l'utilisateur de recevoir son code (RFC 8252).
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
		return &redirectURIError{fmt.Sprintf("redirect_uri %q : http n'est accepté que sur la boucle locale", raw)}
	default:
		return &redirectURIError{fmt.Sprintf("redirect_uri %q : schéma https attendu", raw)}
	}
}

// isLoopbackHost reconnaît les hôtes de la boucle locale.
func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// validateGrantTypes contrôle les flux demandés, et rend ceux qui seront
// enregistrés.
func validateGrantTypes(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return slices.Clone(oauthGrantTypes), nil
	}

	for _, grant := range requested {
		if !slices.Contains(oauthGrantTypes, grant) {
			return nil, fmt.Errorf("grant_types : %q n'est pas proposé par cette instance", grant)
		}
	}

	// authorization_code est ajouté d'office : sans lui, aucun jeton ne peut être
	// obtenu, et un client qui ne demanderait que refresh_token serait
	// enregistré pour ne jamais servir.
	granted := slices.Clone(requested)
	if !slices.Contains(granted, "authorization_code") {
		granted = append(granted, "authorization_code")
	}

	return granted, nil
}

// validateResponseTypes contrôle les types de réponse demandés. Seul « code »
// existe : OAuth 2.1 a retiré les autres.
func validateResponseTypes(requested []string) error {
	for _, responseType := range requested {
		if responseType != "code" {
			return fmt.Errorf("response_types : %q n'est pas proposé, seul \"code\" l'est", responseType)
		}
	}
	return nil
}

// validateRegisteredScopes contrôle les scopes que le client pourra demander.
//
// Enregistrer un scope n'est pas l'obtenir : ce que le client obtiendra
// réellement est borné une seconde fois au consentement, par les droits du
// compte qui autorise. Un client peut donc s'enregistrer pour tout et ne rien
// recevoir.
func validateRegisteredScopes(raw string) ([]string, error) {
	known := scopeStrings(identity.AllScopes())

	requested := strings.Fields(raw)
	if len(requested) == 0 {
		return known, nil
	}

	for _, scope := range requested {
		if !slices.Contains(known, scope) {
			return nil, fmt.Errorf("scope : %q est inconnu de cette instance", scope)
		}
	}

	return requested, nil
}

// clientIDEntropy est la taille en octets de l'identifiant de client tiré au
// hasard. Seize octets, soit 128 bits : de quoi rendre la collision et la
// devinette également hors de portée.
const clientIDEntropy = 16

// newClientID tire un identifiant de client.
func newClientID() (string, error) {
	raw := make([]byte, clientIDEntropy)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("tirage d'un identifiant de client : %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// writeRegistrationError répond au format de la RFC 7591 §3.2.2.
func (h *Handler) writeRegistrationError(w http.ResponseWriter, r *http.Request, status int, code, description string) {
	h.writeJSON(w, r, status, registrationError{Error: code, Description: description})
}

// registrationLimiter borne le nombre d'enregistrements par adresse.
//
// Il partage la forme et les limites du garde-fou de connexion : en mémoire,
// donc remis à zéro au redémarrage, et fondé sur l'adresse de la connexion TCP
// et non sur X-Forwarded-For — un en-tête que l'appelant écrit lui offrirait un
// compteur neuf à chaque requête. Derrière un reverse proxy, toutes les demandes
// partagent donc une seule clé, et la limite devient globale : c'est plus strict,
// pas moins, et cela reste tenable pour un point de terminaison qu'un client
// légitime appelle une fois dans sa vie.
type registrationLimiter struct {
	mu       sync.Mutex
	attempts map[string]registrationCount
	clock    func() time.Time
}

// registrationCount est le compteur d'une adresse.
type registrationCount struct {
	count int
	first time.Time
}

func newRegistrationLimiter(clock func() time.Time) *registrationLimiter {
	if clock == nil {
		clock = time.Now
	}
	return &registrationLimiter{
		attempts: make(map[string]registrationCount),
		clock:    clock,
	}
}

// allow décompte une tentative et dit si elle est permise.
func (l *registrationLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock()

	counter, ok := l.attempts[key]
	switch {
	case !ok:
		l.evict(now)
		counter = registrationCount{first: now}
	case now.Sub(counter.first) >= registrationWindow:
		// La fenêtre est passée : le compteur repart à neuf plutôt que de traîner
		// des tentatives vieilles de plusieurs heures.
		counter = registrationCount{first: now}
	case counter.count >= registrationsPerWindow:
		return false
	}

	counter.count++
	l.attempts[key] = counter

	return true
}

// evict borne la taille de la carte avant d'y ajouter une clé. Les compteurs
// périmés partent d'abord ; s'il n'y en a pas assez, la carte est vidée. Elle se
// reconstruit en une fenêtre, et un enregistrement dynamique n'est pas un chemin
// assez chaud pour que cela se remarque.
//
// À appeler le verrou tenu.
func (l *registrationLimiter) evict(now time.Time) {
	if len(l.attempts) < registrationTrackedCap {
		return
	}

	for key, counter := range l.attempts {
		if now.Sub(counter.first) >= registrationWindow {
			delete(l.attempts, key)
		}
	}
	if len(l.attempts) >= registrationTrackedCap {
		clear(l.attempts)
	}
}
