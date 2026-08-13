package web_test

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/identity"
)

// Valeurs du parcours de référence : deux devis pour un même lot de travaux,
// dont l'un est moins cher de sept cents euros.
const (
	lotTest        = "Charpente"
	entrepriseHaut = "Charpentes du Val"
	entrepriseBas  = "Toiture Ain"
	montantHaut    = "12 500,00"
	montantBas     = "11800,50"
	// Les mêmes montants tels qu'ils s'affichent, avec l'espace insécable étroite
	// de l'imprimerie française.
	montantHautAffiche = "12 500,00"
	montantBasAffiche  = "11 800,50"
)

// nouvelleDemande soumet le formulaire de création et rend la demande créée.
func nouvelleDemande(t *testing.T, s *site, b *browser) devis.DemandeDevis {
	t.Helper()

	result := b.post(devisDemandesTestPath, url.Values{
		"lot":                {lotTest},
		"description":        {"Remplacement de la charpente, 90 m²."},
		"date_envoi":         {"2026-03-02"},
		"artisan_entreprise": {entrepriseHaut, entrepriseBas, ""},
		"artisan_email":      {"contact@val.fr", "", ""},
		"artisan_telephone":  {"", "", ""},
	})
	if result.Status != http.StatusSeeOther {
		t.Fatalf("création de la demande : statut = %d, attendu 303 — corps : %s", result.Status, result.Body)
	}

	demande, ok := s.devis.demandeParLot(lotTest)
	if !ok {
		t.Fatal("la demande n'a pas été enregistrée")
	}

	return demande
}

// devisDemandesTestPath est le point de soumission du formulaire de demande. Il
// est écrit en dur ici, et non repris d'une constante de l'adapter : un test qui
// suivrait le code ne verrait pas une URL changer sous les pieds de ceux qui ont
// mis la page en favori.
const devisDemandesTestPath = "/devis/demandes"

// enregistrerDevis soumet le formulaire d'ajout d'un devis reçu.
func enregistrerDevis(t *testing.T, b *browser, demandeID devis.ID, entreprise, montant string) httpResult {
	t.Helper()

	return b.post("/devis/demandes/"+demandeID.String()+"/devis", url.Values{
		"entreprise":     {entreprise},
		"montant":        {montant},
		"date_reception": {"2026-03-12"},
		"validite_jours": {"30"},
	})
}

// TestDevisRoutesRequireScope est la vérification qui compte le plus de ce lot :
// les routes sont gardées par un scope, et un compte connecté qui ne le détient
// pas est refusé — sur la lecture comme sur l'écriture.
func TestDevisRoutesRequireScope(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	sansScope := addAccountWithoutScopes(t, s)

	b := newBrowser(t, s.handler)
	b.login(sansScope)

	reads := []string{"/devis", "/devis/demandes/nouvelle", "/devis/demandes/peu-importe"}
	for _, target := range reads {
		if result := b.get(target); result.Status != http.StatusForbidden {
			t.Errorf("GET %s : statut = %d, attendu 403", target, result.Status)
		}
	}

	writes := []string{
		"/devis/demandes",
		"/devis/demandes/peu-importe/devis",
		"/devis/propositions/peu-importe/retenir",
		"/devis/propositions/peu-importe/refuser",
	}
	for _, target := range writes {
		if result := b.post(target, url.Values{}); result.Status != http.StatusForbidden {
			t.Errorf("POST %s : statut = %d, attendu 403", target, result.Status)
		}
	}
}

// TestDevisRoutesOpenToCollaborator : le collaborateur détient devis:read et
// devis:write, et va donc au bout du parcours. C'est le pendant du test
// précédent : sans lui, une garde qui refuserait tout le monde passerait pour
// une garde qui fonctionne.
func TestDevisRoutesOpenToCollaborator(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(collaboratorEmail)

	if result := b.get("/devis"); result.Status != http.StatusOK {
		t.Fatalf("GET /devis : statut = %d, attendu 200", result.Status)
	}
	if result := b.get("/devis/demandes/nouvelle"); result.Status != http.StatusOK {
		t.Fatalf("GET /devis/demandes/nouvelle : statut = %d, attendu 200", result.Status)
	}

	demande := nouvelleDemande(t, s, b)
	if result := enregistrerDevis(t, b, demande.ID, entrepriseBas, montantBas); result.Status != http.StatusSeeOther {
		t.Fatalf("enregistrement d'un devis : statut = %d — corps : %s", result.Status, result.Body)
	}
}

// TestDevisAnonymousIsRedirected : la garde par scope ne remplace pas
// l'authentification, elle s'y ajoute. Un anonyme part au formulaire, pas sur un
// refus qui lui apprendrait que la page existe.
func TestDevisAnonymousIsRedirected(t *testing.T) {
	t.Parallel()

	b := newBrowser(t, newSite(t).handler)

	result := b.get("/devis")
	if result.Status != http.StatusSeeOther {
		t.Fatalf("GET /devis anonyme : statut = %d, attendu 303", result.Status)
	}
	if !strings.HasPrefix(result.Location(), "/connexion") {
		t.Errorf("redirection vers %q, attendu /connexion", result.Location())
	}
}

// TestDevisJourney suit le parcours complet, celui que Romain fera : ouvrir une
// consultation, y enregistrer deux devis, les comparer, en retenir un.
func TestDevisJourney(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	// La liste est vide, et propose de créer une demande.
	index := b.get("/devis")
	if !strings.Contains(index.Body, "Aucune demande") {
		t.Errorf("la liste vide ne le dit pas : %s", index.Body)
	}
	if !strings.Contains(index.Body, `href="/devis/demandes/nouvelle"`) {
		t.Error("la liste ne propose pas de créer une demande")
	}

	demande := nouvelleDemande(t, s, b)
	if demande.Lot != lotTest || len(demande.Artisans) != 2 {
		t.Fatalf("demande enregistrée = %+v", demande)
	}

	// Deux devis, saisis dans le désordre des montants.
	for _, devisTest := range []struct{ entreprise, montant string }{
		{entrepriseHaut, montantHaut},
		{entrepriseBas, montantBas},
	} {
		if result := enregistrerDevis(t, b, demande.ID, devisTest.entreprise, devisTest.montant); result.Status != http.StatusSeeOther {
			t.Fatalf("enregistrement de %s : statut = %d — corps : %s", devisTest.entreprise, result.Status, result.Body)
		}
	}

	comparaison := b.get("/devis/demandes/" + demande.ID.String())
	if comparaison.Status != http.StatusOK {
		t.Fatalf("GET de la comparaison : statut = %d", comparaison.Status)
	}

	// Les montants sont affichés en euros, à partir des centimes stockés.
	for _, montant := range []string{montantHautAffiche, montantBasAffiche} {
		if !strings.Contains(comparaison.Body, montant) {
			t.Errorf("la comparaison n'affiche pas %q", montant)
		}
	}
	// Et le moins-disant vient en premier.
	if strings.Index(comparaison.Body, entrepriseBas) > strings.Index(comparaison.Body, entrepriseHaut) {
		t.Error("la comparaison ne place pas le moins-disant en tête")
	}

	// La décision : retenir le moins cher.
	moinsDisant, ok := s.devis.devisParEntreprise(entrepriseBas)
	if !ok {
		t.Fatal("le devis du moins-disant est introuvable")
	}

	decision := b.post("/devis/propositions/"+moinsDisant.ID.String()+"/retenir", url.Values{})
	if decision.Status != http.StatusSeeOther {
		t.Fatalf("retenue : statut = %d — corps : %s", decision.Status, decision.Body)
	}
	if !strings.Contains(decision.Location(), "avis=devis_retenu") {
		t.Errorf("redirection après retenue = %q", decision.Location())
	}

	// Le concurrent est refusé par ricochet.
	statuts := map[string]devis.Statut{entrepriseBas: devis.StatutRetenu, entrepriseHaut: devis.StatutRefuse}
	for entreprise, want := range statuts {
		proposition, found := s.devis.devisParEntreprise(entreprise)
		if !found {
			t.Fatalf("devis de %s introuvable", entreprise)
		}
		if proposition.Statut != want {
			t.Errorf("statut de %s = %q, attendu %q", entreprise, proposition.Statut, want)
		}
		if proposition.DecidedBy == "" || proposition.DecidedAt.IsZero() {
			t.Errorf("la décision sur %s n'est pas tracée : %q, %s", entreprise, proposition.DecidedBy, proposition.DecidedAt)
		}
	}

	// La page dit que la comparaison est close et ne propose plus d'ajout.
	apresDecision := b.get("/devis/demandes/" + demande.ID.String())
	if !strings.Contains(apresDecision.Body, "Comparaison close") {
		t.Error("la page ne dit pas que la comparaison est close")
	}
	if strings.Contains(apresDecision.Body, `name="date_reception"`) {
		t.Error("le formulaire d'ajout est encore proposé sur une consultation close")
	}

	// Et la liste résume la décision.
	if body := b.get("/devis").Body; !strings.Contains(body, "Retenu : "+entrepriseBas) {
		t.Errorf("la liste n'annonce pas le devis retenu : %s", body)
	}
}

// TestDevisRefusedOnClosedDemande : l'invariant tient aussi par la porte HTTP,
// et le refus est un message, pas une erreur 500.
func TestDevisRefusedOnClosedDemande(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	demande := nouvelleDemande(t, s, b)
	enregistrerDevis(t, b, demande.ID, entrepriseBas, montantBas)

	retenu, _ := s.devis.devisParEntreprise(entrepriseBas)
	if result := b.post("/devis/propositions/"+retenu.ID.String()+"/retenir", url.Values{}); result.Status != http.StatusSeeOther {
		t.Fatalf("retenue : statut = %d", result.Status)
	}

	refus := enregistrerDevis(t, b, demande.ID, entrepriseHaut, montantHaut)
	if refus.Status != http.StatusUnprocessableEntity {
		t.Fatalf("ajout sur une demande close : statut = %d, attendu 422", refus.Status)
	}
	if !strings.Contains(html.UnescapeString(refus.Body), "consultation est close") {
		t.Errorf("le refus n'est pas expliqué : %s", refus.Body)
	}
	if _, exists := s.devis.devisParEntreprise(entrepriseHaut); exists {
		t.Error("le devis a été enregistré malgré le refus")
	}
}

// TestDecisionOnDecidedDevisRedirectsWithAvis : deux personnes qui tranchent en
// même temps — ou un double clic. La seconde décision ne casse rien, elle ramène
// à la comparaison avec l'état réel.
func TestDecisionOnDecidedDevisRedirectsWithAvis(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	demande := nouvelleDemande(t, s, b)
	enregistrerDevis(t, b, demande.ID, entrepriseBas, montantBas)

	proposition, _ := s.devis.devisParEntreprise(entrepriseBas)
	target := "/devis/propositions/" + proposition.ID.String() + "/retenir"

	if result := b.post(target, url.Values{}); result.Status != http.StatusSeeOther {
		t.Fatalf("première retenue : statut = %d", result.Status)
	}

	second := b.post(target, url.Values{})
	if second.Status != http.StatusSeeOther {
		t.Fatalf("seconde retenue : statut = %d, attendu 303", second.Status)
	}
	if !strings.Contains(second.Location(), "avis=deja_tranche") {
		t.Errorf("redirection = %q, attendu l'avis « déjà tranché »", second.Location())
	}

	// Et le message est bien affiché à l'arrivée.
	page := b.get(second.Location())
	if !strings.Contains(html.UnescapeString(page.Body), "déjà été tranché") {
		t.Errorf("la page d'arrivée n'affiche pas l'avis : %s", page.Body)
	}
}

// TestRejectLeavesComparaisonOpen : refuser une offre n'en choisit aucune, et le
// formulaire d'ajout reste proposé.
func TestRejectLeavesComparaisonOpen(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	demande := nouvelleDemande(t, s, b)
	enregistrerDevis(t, b, demande.ID, entrepriseHaut, montantHaut)

	ecarte, _ := s.devis.devisParEntreprise(entrepriseHaut)
	result := b.post("/devis/propositions/"+ecarte.ID.String()+"/refuser", url.Values{})
	if result.Status != http.StatusSeeOther || !strings.Contains(result.Location(), "avis=devis_refuse") {
		t.Fatalf("refus : statut = %d, redirection = %q", result.Status, result.Location())
	}

	page := b.get("/devis/demandes/" + demande.ID.String())
	if strings.Contains(page.Body, "Comparaison close") {
		t.Error("un refus isolé a clos la comparaison")
	}
	if !strings.Contains(page.Body, `name="date_reception"`) {
		t.Error("le formulaire d'ajout n'est plus proposé après un simple refus")
	}
}

// TestDevisFormRejectsInvalidInput : les saisies fautives reviennent en 422 avec
// un message du catalogue, la saisie conservée, et rien d'écrit.
func TestDevisFormRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		fields  url.Values
		message string
	}{
		"montant illisible": {
			fields:  url.Values{"entreprise": {entrepriseBas}, "montant": {"douze mille"}, "date_reception": {"2026-03-12"}},
			message: "doit être un nombre",
		},
		"montant nul": {
			fields:  url.Values{"entreprise": {entrepriseBas}, "montant": {"0"}, "date_reception": {"2026-03-12"}},
			message: "strictement positif",
		},
		"entreprise vide": {
			fields:  url.Values{"entreprise": {"  "}, "montant": {montantBas}, "date_reception": {"2026-03-12"}},
			message: "nom de l'entreprise est obligatoire",
		},
		"date de réception absente": {
			fields:  url.Values{"entreprise": {entrepriseBas}, "montant": {montantBas}, "date_reception": {""}},
			message: "date n'a pas une forme valide",
		},
		"email d'artisan invalide": {
			fields: url.Values{
				"entreprise": {entrepriseBas}, "montant": {montantBas},
				"date_reception": {"2026-03-12"}, "email": {"pas-une-adresse"},
			},
			message: "adresse email n'a pas une forme valide",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newSite(t)
			b := newBrowser(t, s.handler)
			b.login(ownerEmail)
			demande := nouvelleDemande(t, s, b)

			result := b.post("/devis/demandes/"+demande.ID.String()+"/devis", tc.fields)
			if result.Status != http.StatusUnprocessableEntity {
				t.Fatalf("statut = %d, attendu 422 — corps : %s", result.Status, result.Body)
			}
			// Le corps est déséchappé avant comparaison : html/template écrit les
			// apostrophes en &#39;, et un test qui l'ignorerait vérifierait
			// l'échappement plutôt que le message.
			if !strings.Contains(html.UnescapeString(result.Body), tc.message) {
				t.Errorf("le message d'erreur attendu (%q) est absent : %s", tc.message, result.Body)
			}
			propositions, listErr := s.devis.ListDevis(t.Context())
			if listErr != nil {
				t.Fatalf("ListDevis() échoué : %v", listErr)
			}
			if len(propositions) != 0 {
				t.Errorf("%d devis écrit(s) malgré le refus", len(propositions))
			}
		})
	}
}

// TestDemandeFormRejectsEmptyLot : le formulaire de consultation refuse de la
// même façon, en réaffichant ce qui a été saisi.
func TestDemandeFormRejectsEmptyLot(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	result := b.post(devisDemandesTestPath, url.Values{
		"lot":                {"   "},
		"description":        {"Une description qui ne doit pas être perdue."},
		"date_envoi":         {"2026-03-02"},
		"artisan_entreprise": {entrepriseBas},
	})

	if result.Status != http.StatusUnprocessableEntity {
		t.Fatalf("statut = %d, attendu 422", result.Status)
	}
	if !strings.Contains(html.UnescapeString(result.Body), "intitulé du lot de travaux est obligatoire") {
		t.Errorf("le message d'erreur est absent : %s", result.Body)
	}
	if !strings.Contains(result.Body, "Une description qui ne doit pas être perdue.") {
		t.Error("la saisie n'est pas réaffichée")
	}
	if !strings.Contains(result.Body, entrepriseBas) {
		t.Error("les artisans saisis ne sont pas réaffichés")
	}
	demandes, listErr := s.devis.ListDemandes(t.Context())
	if listErr != nil {
		t.Fatalf("ListDemandes() échoué : %v", listErr)
	}
	if len(demandes) != 0 {
		t.Errorf("%d demande(s) écrite(s) malgré le refus", len(demandes))
	}
}

// TestUnknownDemandeIsNotFound : une URL de demande inconnue rend un 404, et non
// une page d'erreur interne — l'identifiant illisible compris, qui ne désigne
// rien lui non plus.
func TestUnknownDemandeIsNotFound(t *testing.T) {
	t.Parallel()

	b := newBrowser(t, newSite(t).handler)
	b.login(ownerEmail)

	for _, target := range []string{
		"/devis/demandes/6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5",
		"/devis/demandes/pas-un-identifiant",
	} {
		if result := b.get(target); result.Status != http.StatusNotFound {
			t.Errorf("GET %s : statut = %d, attendu 404", target, result.Status)
		}
	}
}

// TestDecisionSurDevisInconnu : même règle sur le chemin d'écriture.
func TestDecisionSurDevisInconnu(t *testing.T) {
	t.Parallel()

	b := newBrowser(t, newSite(t).handler)
	b.login(ownerEmail)

	result := b.post("/devis/propositions/6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5/retenir", url.Values{})
	if result.Status != http.StatusNotFound {
		t.Errorf("statut = %d, attendu 404", result.Status)
	}
}

// TestDecisionAvecHTMX : avec HTMX, la redirection passe par l'en-tête
// HX-Redirect — un 303 vers une page entière serait inséré dans le fragment que
// la bibliothèque attendait. Sans JavaScript, c'est la redirection HTTP
// ordinaire, vérifiée par les autres tests.
func TestDecisionAvecHTMX(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	demande := nouvelleDemande(t, s, b)
	enregistrerDevis(t, b, demande.ID, entrepriseBas, montantBas)

	proposition, _ := s.devis.devisParEntreprise(entrepriseBas)
	result := b.post("/devis/propositions/"+proposition.ID.String()+"/retenir", url.Values{}, "HX-Request", "true")

	if result.Status != http.StatusNoContent {
		t.Fatalf("statut = %d, attendu 204", result.Status)
	}
	target := result.Header.Get("HX-Redirect")
	if !strings.Contains(target, demande.ID.String()) || !strings.Contains(target, "avis=devis_retenu") {
		t.Errorf("HX-Redirect = %q", target)
	}
	if result.Location() != "" {
		t.Errorf("une redirection HTTP a aussi été émise : %q", result.Location())
	}
}

// TestDevisPagesHaveNoMissingTranslation : aucune chaîne de l'interface n'est
// écrite en dur, donc aucune ne peut manquer au catalogue sans que le marqueur
// !comme.ceci! apparaisse. Les trois pages du domaine y passent, dans leurs
// états significatifs.
func TestDevisPagesHaveNoMissingTranslation(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	demande := nouvelleDemande(t, s, b)
	enregistrerDevis(t, b, demande.ID, entrepriseHaut, montantHaut)
	enregistrerDevis(t, b, demande.ID, entrepriseBas, montantBas)

	comparaisonPath := "/devis/demandes/" + demande.ID.String()
	targets := []string{
		"/devis",
		"/devis?avis=demande_creee",
		"/devis/demandes/nouvelle",
		comparaisonPath,
		comparaisonPath + "?avis=devis_ajoute",
		comparaisonPath + "?avis=deja_tranche",
	}
	for _, target := range targets {
		if marker := findMarker(b.get(target).Body); marker != "" {
			t.Errorf("%s affiche une traduction manquante : %s", target, marker)
		}
	}

	// Puis les mêmes pages une fois la comparaison close, et la page de refus
	// d'autorisation.
	proposition, _ := s.devis.devisParEntreprise(entrepriseBas)
	b.post("/devis/propositions/"+proposition.ID.String()+"/retenir", url.Values{})

	for _, target := range []string{"/devis", comparaisonPath} {
		if marker := findMarker(b.get(target).Body); marker != "" {
			t.Errorf("%s (comparaison close) affiche une traduction manquante : %s", target, marker)
		}
	}

	refuse := newBrowser(t, s.handler)
	refuse.login(addAccountWithoutScopes(t, s))
	if marker := findMarker(refuse.get("/devis").Body); marker != "" {
		t.Errorf("la page de refus affiche une traduction manquante : %s", marker)
	}
}

// addAccountWithoutScopes ajoute un compte dont le rôle ne porte aucun scope.
//
// Il est posé directement dans le dépôt, parce que le service refuse — à raison —
// de créer un compte au rôle inconnu. C'est pourtant l'état qu'on veut
// éprouver : celui d'un compte que le catalogue des rôles ne reconnaît plus,
// après un renommage ou une rétrogradation. La table des rôles lui donne alors
// zéro scope, et c'est la garde de route qui doit s'y opposer.
func addAccountWithoutScopes(t *testing.T, s *site) string {
	t.Helper()

	const email = "observateur@exemple.fr"

	instant := time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC)
	s.repo.accounts["00000000-0000-4000-8000-000000000001"] = identity.User{
		ID:           "00000000-0000-4000-8000-000000000001",
		Email:        email,
		DisplayName:  "Compte sans droits",
		PasswordHash: identity.PasswordHash("trivial:" + password),
		Role:         identity.Role("observateur"),
		Active:       true,
		CreatedAt:    instant,
		UpdatedAt:    instant,
	}

	return email
}
