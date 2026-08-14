package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ory/fosite"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// Champs du formulaire de consentement.
//
// Ils sont en français, comme ceux du formulaire de connexion : ce sont les
// seuls paramètres de tout le serveur d'autorisation qu'un humain remplit, par
// un clic sur l'un des deux boutons. Les paramètres du protocole, eux, gardent
// leurs noms de RFC.
const (
	fieldOAuthDecision = "decision"
	fieldOAuthRequest  = "requete"
)

// Valeurs du champ decision.
const (
	decisionAllow  = "autoriser"
	decisionRefuse = "refuser"
)

// paramCodeChallenge et paramCodeChallengeMethod sont les paramètres PKCE, lus
// avant fosite pour ne pas afficher une page de consentement à une demande qui
// sera de toute façon refusée.
const (
	paramCodeChallenge       = "code_challenge"
	paramCodeChallengeMethod = "code_challenge_method"
	// paramResource est l'indicateur de ressource de la RFC 8707. La
	// spécification MCP demande aux clients de l'envoyer systématiquement.
	paramResource = "resource"
	// paramIssuer est le paramètre iss de la RFC 9207, ajouté à la réponse
	// d'autorisation.
	paramIssuer = "iss"
	// challengeMethodS256 est la seule transformation PKCE acceptée.
	challengeMethodS256 = "S256"
)

// errInvalidTarget signale une ressource demandée que ce serveur ne sert pas.
//
// fosite ne fournit pas cette erreur : la RFC 8707 est postérieure à la partie
// de la bibliothèque qui les énumère. Elle est donc construite ici, au format
// que fosite sait écrire.
var errInvalidTarget = &fosite.RFC6749Error{
	ErrorField:       "invalid_target",
	DescriptionField: "L'indicateur de ressource doit désigner l'URL canonique du serveur MCP de cette instance.",
	CodeField:        http.StatusBadRequest,
}

// consentData est la charge utile de la page de consentement.
type consentData struct {
	// ClientName est le nom que le client a déclaré à son enregistrement. Il
	// vient d'un inconnu : le gabarit l'échappe, comme toute donnée.
	ClientName string
	// ClientID est l'identifiant que le serveur a attribué au client. Il est
	// affiché parce que le nom, lui, ne distingue rien : l'enregistrement étant
	// ouvert, deux clients peuvent porter le même — leurs identifiants, non.
	ClientID string
	// RegisteredAt est la date d'enregistrement du client, déjà formatée, ou une
	// chaîne vide si le client ne l'annonce pas. C'est le fait le plus parlant de
	// la page contre une usurpation de nom : un agent connu de longue date ne
	// s'est pas enregistré ce matin.
	RegisteredAt string
	// FirstGrant dit que ce compte n'a encore jamais autorisé ce client. Le
	// distinguer est ce qui permet à la personne de reconnaître, ou non, la
	// relation qu'on lui demande de renouveler.
	FirstGrant bool
	// Scopes est ce qui sera réellement accordé — l'intersection de ce que le
	// client demande et de ce que le compte détient. Montrer la demande brute
	// ferait consentir à des droits qui ne seront pas donnés.
	Scopes []consentScope
	// Ignored est ce que le client a demandé sans l'obtenir. L'afficher évite la
	// surprise d'un agent qui échoue plus tard sans qu'on sache pourquoi.
	Ignored []consentScope
	// Request est la requête d'autorisation d'origine, telle qu'elle est arrivée.
	// Elle repart dans un champ caché et est revalidée intégralement à la
	// soumission.
	Request string
	// AllowValue et RefuseValue nomment les deux décisions possibles.
	AllowValue  string
	RefuseValue string
	// FieldDecision et FieldRequest nomment les champs, pour que le gabarit
	// n'ait pas à répéter des chaînes que le code connaît déjà.
	FieldDecision string
	FieldRequest  string
	// Action est la cible du formulaire.
	Action string
}

// consentScope est un droit tel qu'il s'affiche.
type consentScope struct {
	// Label est l'intitulé traduit du scope.
	Label string
	// Name est le scope lui-même, affiché en second pour qui veut vérifier.
	Name string
}

// handleOAuthAuthorizeForm affiche la page de consentement.
//
// La route n'est pas dans les exceptions de [isPublicPath], et c'est ce qui fait
// tout le travail d'authentification : un visiteur sans session est renvoyé vers
// /connexion avec la demande d'autorisation en paramètre « suite », et y revient
// une fois connecté. Rien n'est à écrire ici pour cela.
func (h *Handler) handleOAuthAuthorizeForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	request, ok := h.newAuthorizeRequest(w, r)
	if !ok {
		return
	}

	actor := ActorFromContext(ctx)
	granted, ignored, allowed := grantableScopes(request, actor)
	if !allowed {
		h.renderMCPRefused(w, r)
		return
	}

	translator := h.catalog.Translator(r.Header.Get("Accept-Language"))
	client := request.GetClient()

	h.render(w, r, pageOAuthConsent, http.StatusOK, consentData{
		ClientName:    clientDisplayName(client),
		ClientID:      client.GetID(),
		RegisteredAt:  clientRegistrationDate(client),
		FirstGrant:    h.firstGrant(r, actor, client.GetID()),
		Scopes:        consentScopes(translator, granted),
		Ignored:       consentScopes(translator, ignored),
		Request:       r.URL.RawQuery,
		AllowValue:    decisionAllow,
		RefuseValue:   decisionRefuse,
		FieldDecision: fieldOAuthDecision,
		FieldRequest:  fieldOAuthRequest,
		Action:        oauthAuthorizePath,
	})
}

// handleOAuthAuthorizeDecision traite le clic de l'utilisateur.
//
// La demande d'autorisation est **revalidée entièrement**, à partir du champ
// caché, plutôt que retenue entre les deux requêtes. Le champ est donc
// modifiable par qui soumet le formulaire, et c'est sans conséquence : tout ce
// qu'il contient repasse par fosite — client existant, adresse de retour
// enregistrée, PKCE — puis par les droits du compte connecté. Rien n'y est cru
// sur parole, et bricoler ce champ ne donne pas davantage que lancer soi-même
// une nouvelle demande d'autorisation.
func (h *Handler) handleOAuthAuthorizeDecision(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		h.fail(r, fmt.Errorf("lecture du formulaire de consentement : %w", err))
		h.render(w, r, pageInternalError, http.StatusInternalServerError, nil)
		return
	}

	replay := replayAuthorizeRequest(r)

	request, ok := h.newAuthorizeRequest(w, replay)
	if !ok {
		return
	}

	actor := ActorFromContext(ctx)
	granted, _, allowed := grantableScopes(request, actor)
	if !allowed {
		h.renderMCPRefused(w, r)
		return
	}

	if r.PostFormValue(fieldOAuthDecision) != decisionAllow {
		// Le refus est une réponse du protocole, pas une erreur d'Avanti : il
		// repart vers le client sous la forme access_denied, qui lui permet
		// d'expliquer proprement à l'utilisateur que rien ne sera connecté.
		h.oauth.provider.WriteAuthorizeError(ctx, w, request, fosite.ErrAccessDenied)
		return
	}

	for _, scope := range granted {
		request.GrantScope(scope)
	}

	// Le consentement est noté avant que la réponse ne soit fabriquée, parce que
	// ce qu'il enregistre est la décision de la personne — elle vient de cliquer
	// « Autoriser », et c'est vrai que la redirection aboutisse ou non. Dans
	// l'autre ordre, une panne entre l'émission du code et l'écriture laisserait
	// un consentement réel sans trace, et la page annoncerait « première
	// autorisation » à un client déjà autorisé : l'indicateur perdrait son sens
	// dans le seul sens qui compte, celui qui rassure à tort.
	if err := h.oauth.storage.RecordGrant(ctx, actor.UserID().String(),
		request.GetClient().GetID(), h.oauth.clock()); err != nil {
		h.fail(r, fmt.Errorf("enregistrement du consentement : %w", err))
		h.oauth.provider.WriteAuthorizeError(ctx, w, request, fosite.ErrServerError)
		return
	}

	// La session est ce que le jeton portera. Elle ne contient que le sujet :
	// l'identifiant du compte suffit à retrouver tout le reste au moment de la
	// vérification, et recopier l'email ou le rôle en ferait des instantanés
	// figés à l'émission — donc faux dès le premier changement.
	session := &fosite.DefaultSession{Subject: actor.UserID().String()}

	response, err := h.oauth.provider.NewAuthorizeResponse(ctx, request, session)
	if err != nil {
		h.oauth.provider.WriteAuthorizeError(ctx, w, request, err)
		return
	}

	// RFC 9207 : la réponse annonce qui l'a émise, ce qui permet au client de
	// détecter un code qui viendrait d'un autre serveur d'autorisation que celui
	// qu'il croit interroger. Le document de métadonnées le déclare, donc un
	// client conforme le vérifie.
	response.AddParameter(paramIssuer, h.oauth.issuer)

	h.oauth.provider.WriteAuthorizeResponse(ctx, w, request, response)
}

// newAuthorizeRequest analyse et contrôle une demande d'autorisation.
//
// Le booléen rendu dit si l'appelant peut continuer ; quand il est faux, la
// réponse a déjà été écrite — en erreur redirigée vers le client quand une
// adresse de retour valide est connue, en page d'erreur sinon. C'est fosite qui
// tranche entre les deux, et c'est ce qu'on veut : rediriger une erreur vers une
// adresse non vérifiée serait une redirection ouverte.
func (h *Handler) newAuthorizeRequest(w http.ResponseWriter, r *http.Request) (fosite.AuthorizeRequester, bool) {
	ctx := r.Context()

	request, err := h.oauth.provider.NewAuthorizeRequest(ctx, r)
	if err != nil {
		h.oauth.provider.WriteAuthorizeError(ctx, w, request, err)
		return nil, false
	}

	if err := h.checkAuthorizeRequest(request); err != nil {
		h.oauth.provider.WriteAuthorizeError(ctx, w, request, err)
		return nil, false
	}

	return request, true
}

// checkAuthorizeRequest applique les exigences propres à Avanti, avant que
// l'utilisateur ne voie quoi que ce soit.
//
// fosite refuserait de lui-même une demande sans PKCE, mais seulement au moment
// d'émettre le code — c'est-à-dire après le consentement. Contrôler ici évite de
// faire lire et approuver une demande qui ne pouvait pas aboutir.
func (h *Handler) checkAuthorizeRequest(request fosite.AuthorizeRequester) error {
	form := request.GetRequestForm()

	if form.Get(paramCodeChallenge) == "" {
		return fosite.ErrInvalidRequest.WithHint(
			"Le paramètre code_challenge est obligatoire : ce serveur exige PKCE.")
	}
	if method := form.Get(paramCodeChallengeMethod); method != challengeMethodS256 {
		return fosite.ErrInvalidRequest.WithHintf(
			"code_challenge_method doit valoir %s ; %q est refusé.", challengeMethodS256, method)
	}

	// Le scope mcp est la porte de tout ce serveur d'autorisation : il n'existe
	// que pour donner accès à Avanti par MCP. Une demande qui ne le réclame pas
	// obtiendrait un jeton que le serveur MCP refuserait ensuite, sans que
	// personne ne comprenne pourquoi.
	if !request.GetRequestedScopes().Has(identity.ScopeMCP.String()) {
		return fosite.ErrInvalidScope.WithHintf(
			"Le scope %q est obligatoire : ce serveur d'autorisation ne sert que l'accès par MCP.",
			identity.ScopeMCP)
	}

	return h.checkResource(form.Get(paramResource))
}

// checkResource contrôle l'indicateur de ressource de la RFC 8707.
//
// La spécification MCP demande au client d'envoyer « resource » à chaque
// demande, pour que le jeton soit lié au serveur auquel il est destiné. La
// seule valeur acceptée est l'URL CANONIQUE du serveur MCP de cette instance —
// celle que le document Protected Resource Metadata (RFC 9728) annonce, et que
// cmd/avanti transmet à la construction ([Options.MCPResource]). C'est la
// protection contre le « député confus », où un serveur MCP malveillant ferait
// émettre à son profit un jeton valable ailleurs — et elle est resserrée à la
// ressource exacte, plus seulement à l'instance : désigner l'instance nue est
// désormais refusé, un jeton d'Avanti ne vaut que pour son serveur MCP.
//
// La comparaison tolère les seules variations de forme que la RFC 3986 déclare
// équivalentes, plus une : schéma et hôte insensibles à la casse, port par
// défaut du schéma (« :443 » en https, « :80 » en http) équivalent à son
// absence, et barre finale du chemin acceptée — certains clients l'ajoutent.
// Une requête ou un fragment dans la valeur la font refuser : la RFC 8707
// interdit le fragment, et la forme canonique n'a pas de requête.
//
// L'absence du paramètre reste acceptée : la RFC 8707 le laisse facultatif, et
// le refuser fermerait la porte aux clients OAuth qui ne le connaissent pas
// sans rien protéger de plus — un jeton sans audience ne vaut que ce que ce
// serveur en fait, et il ne le présente qu'à lui-même.
func (h *Handler) checkResource(resource string) error {
	if resource == "" {
		return nil
	}

	parsed, err := url.Parse(resource)
	if err != nil || !parsed.IsAbs() {
		return errInvalidTarget
	}

	canonical, err := url.Parse(h.oauth.mcpResource)
	if err != nil {
		return errInvalidTarget
	}

	switch {
	case !strings.EqualFold(parsed.Scheme, canonical.Scheme),
		normalizedHost(parsed) != normalizedHost(canonical),
		strings.TrimSuffix(parsed.Path, "/") != canonical.Path,
		parsed.RawQuery != "",
		parsed.Fragment != "" || parsed.RawFragment != "":
		return errInvalidTarget
	default:
		return nil
	}
}

// normalizedHost rend l'hôte d'une URL en minuscules, port par défaut du
// schéma retiré : « avanti.test:443 » en https et « avanti.test » désignent le
// même serveur (RFC 3986 §6.2.3).
func normalizedHost(u *url.URL) string {
	host := strings.ToLower(u.Host)

	switch strings.ToLower(u.Scheme) {
	case "https":
		return strings.TrimSuffix(host, ":443")
	case "http":
		return strings.TrimSuffix(host, ":80")
	default:
		return host
	}
}

// grantableScopes répartit les scopes demandés entre ceux que le compte peut
// accorder et les autres.
//
// Le dernier retour dit si le compte a le droit d'ouvrir un accès agent IA. Un
// collaborateur ne l'a pas : la table des rôles ne lui donne pas le scope mcp, et
// aucune combinaison de paramètres ne le lui donnera — c'est un refus de compte,
// pas un refus de demande.
//
// La fonction ne traduit rien et ne connaît pas la requête HTTP : c'est la
// décision d'autorisation, et elle doit pouvoir se lire sans démêler ce qui
// relève de l'affichage.
func grantableScopes(request fosite.AuthorizeRequester, actor identity.Actor) (granted, ignored []string, allowed bool) {
	if !actor.Allows(identity.ScopeMCP) {
		return nil, nil, false
	}

	for _, requested := range request.GetRequestedScopes() {
		if actor.Allows(identity.Scope(requested)) {
			granted = append(granted, requested)
			continue
		}
		ignored = append(ignored, requested)
	}

	return granted, ignored, true
}

// consentScopes habille des scopes pour l'affichage.
//
// La traduction se fait ici plutôt que dans le gabarit parce que l'identifiant
// de message est calculé : un gabarit ne peut pas l'écrire en clair, et le test
// qui relie les gabarits au catalogue ne le verrait donc pas. Le lien est assuré
// autrement — un test parcourt [identity.AllScopes] et exige une traduction pour
// chacun.
func consentScopes(translator *Translator, scopes []string) []consentScope {
	out := make([]consentScope, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, consentScope{
			Name:  scope,
			Label: translator.T(scopeMessageID(scope)),
		})
	}
	return out
}

// scopeMessageID traduit un scope en identifiant de message du catalogue.
//
// Les deux-points du scope deviennent un point, qui est le séparateur des clés
// i18n : « devis:read » se lit « oauth.scope.devis.read » dans locales/fr.json.
// Un scope sans traduction rend le marqueur voyant de [Translator.T], donc se
// remarque à la première page affichée.
func scopeMessageID(scope string) string {
	return "oauth.scope." + strings.ReplaceAll(scope, ":", ".")
}

// clientDisplayName rend le nom sous lequel le client se présente.
//
// Un client qui n'annonce pas de nom s'affiche sous son identifiant : moins
// parlant, mais jamais trompeur — et c'est le seul cas où l'utilisateur voit une
// chaîne technique sur cette page.
func clientDisplayName(client fosite.Client) string {
	if named, ok := client.(namedClient); ok {
		if name := named.GetName(); name != "" {
			return name
		}
	}
	return client.GetID()
}

// clientRegistrationDate rend la date d'enregistrement du client telle qu'elle
// s'affiche, ou une chaîne vide si le client ne l'annonce pas.
//
// Le format est celui que lit un francophone, et il s'arrête au jour : l'heure
// n'apporterait rien à la décision, et une date longue noierait le fait utile —
// « aujourd'hui » ou « il y a six mois ».
func clientRegistrationDate(client fosite.Client) string {
	dated, ok := client.(datedClient)
	if !ok {
		return ""
	}

	registeredAt := dated.GetRegisteredAt()
	if registeredAt.IsZero() {
		return ""
	}

	return registeredAt.Local().Format("02/01/2006")
}

// firstGrant dit si le compte connecté n'a encore jamais autorisé ce client.
//
// Une erreur de lecture ne fait pas échouer la page : elle est journalisée, et
// l'indicateur retombe du côté prudent. « Première autorisation » invite à
// relire le nom et l'identifiant ; « vous avez déjà autorisé ce client »
// invite à cliquer sans regarder. Se tromper dans le premier sens coûte quelques
// secondes d'attention, se tromper dans le second peut coûter l'accès au
// dossier.
func (h *Handler) firstGrant(r *http.Request, actor identity.Actor, clientID string) bool {
	granted, err := h.oauth.storage.HasGrant(r.Context(), actor.UserID().String(), clientID)
	if err != nil {
		h.fail(r, fmt.Errorf("lecture des consentements antérieurs du client %s : %w", clientID, err))
		return true
	}

	return !granted
}

// replayAuthorizeRequest reconstruit la demande d'autorisation d'origine à
// partir du champ caché du formulaire.
//
// La requête est rejouée en GET, avec la requête d'origine en chaîne de
// paramètres : c'est exactement ce que fosite a déjà analysé une première fois,
// et cela évite que les champs du formulaire de consentement — decision, requete
// — ne se mêlent aux paramètres du protocole. L'enveloppe de la requête réelle
// est conservée, parce que l'hôte et l'adresse de l'appelant restent ceux de la
// requête en cours.
func replayAuthorizeRequest(r *http.Request) *http.Request {
	replay := r.Clone(r.Context())
	replay.Method = http.MethodGet
	replay.URL = &url.URL{
		Scheme:   r.URL.Scheme,
		Host:     r.URL.Host,
		Path:     oauthAuthorizePath,
		RawQuery: r.PostFormValue(fieldOAuthRequest),
	}
	replay.Body = http.NoBody
	replay.ContentLength = 0
	replay.Header.Del("Content-Type")
	replay.Form = nil
	replay.PostForm = nil
	replay.MultipartForm = nil

	return replay
}

// renderMCPRefused explique à l'utilisateur que son compte n'ouvre pas l'accès
// agent IA.
//
// C'est une page, pas une redirection d'erreur vers le client, et c'est un écart
// assumé au réflexe « toute erreur d'autorisation repart vers le client ». Le
// motif est le destinataire : renvoyer access_denied afficherait à
// l'utilisateur un message générique du côté de l'agent, sans jamais lui dire
// que c'est son rôle qui bloque ni à qui le demander. Le client, lui, n'apprend
// rien de plus par une redirection que par l'absence de retour.
func (h *Handler) renderMCPRefused(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, pageOAuthRefused, http.StatusForbidden, nil)
}
