package web_test

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/planning"
)

// posterEtape soumet le formulaire de création d'étape.
func posterEtape(t *testing.T, b *browser, nom, debut, fin string, dependances ...string) httpResult {
	t.Helper()

	fields := url.Values{
		"nom":         {nom},
		"debut_prevu": {debut},
		"fin_prevue":  {fin},
	}
	for _, dep := range dependances {
		fields.Add("dependances", dep)
	}

	return b.post("/planning/etapes", fields)
}

// etapeEnregistree retrouve l'étape créée sous ce nom, ou échoue.
func etapeEnregistree(t *testing.T, s *site, nom string) planning.Etape {
	t.Helper()

	etape, ok := s.planning.etapeParNom(nom)
	if !ok {
		t.Fatalf("l'étape %q n'a pas été enregistrée", nom)
	}

	return etape
}

// transition poste une action à garde optimiste sur un élément du planning.
func transition(t *testing.T, b *browser, target, modifieLe string) httpResult {
	t.Helper()

	return b.post(target, url.Values{"modifie_le": {modifieLe}})
}

// modifieLeDe rend la garde optimiste courante d'une étape, au format que les
// formulaires portent.
func modifieLeDe(t *testing.T, s *site, nom string) string {
	t.Helper()

	return etapeEnregistree(t, s, nom).UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
}

// TestPlanningCollaboratorHasAccess vérifie EXPLICITEMENT ce que la table des
// rôles promet : le collaborateur — l'architecte — détient planning:read ET
// planning:write. C'est le domaine qu'il travaille, lecture et écriture.
func TestPlanningCollaboratorHasAccess(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(collaboratorEmail)

	result := b.get("/planning")
	if result.Status != http.StatusOK {
		t.Fatalf("GET /planning (collaborateur) : statut = %d, attendu 200", result.Status)
	}
	// Le formulaire de création est construit : le scope d'écriture est bien là.
	if !strings.Contains(result.Body, `action="/planning/etapes"`) {
		t.Error("le collaborateur ne voit pas le formulaire de création d'étape")
	}

	if result := posterEtape(t, b, "Charpente", "2500-01-01", "2500-01-20"); result.Status != http.StatusSeeOther {
		t.Errorf("POST /planning/etapes (collaborateur) : statut = %d, attendu 303 — corps : %s", result.Status, result.Body)
	}
}

// TestPlanningRoutesRequireScope : les deux rôles fournis portent les scopes
// planning, donc le refus par scope se vérifie avec un compte dont le rôle
// n'en porte aucun — le même « observateur » que pour les devis.
func TestPlanningRoutesRequireScope(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(addAccountWithoutScopes(t, s))

	for _, target := range []string{"/planning", "/planning/etapes/peu-importe/modifier"} {
		if result := b.get(target); result.Status != http.StatusForbidden {
			t.Errorf("GET %s (sans scope) : statut = %d, attendu 403", target, result.Status)
		}
	}

	writes := []string{
		"/planning/etapes",
		"/planning/etapes/peu-importe",
		"/planning/etapes/peu-importe/demarrer",
		"/planning/etapes/peu-importe/terminer",
		"/planning/jalons",
		"/planning/jalons/peu-importe/atteindre",
	}
	for _, target := range writes {
		if result := b.post(target, url.Values{}); result.Status != http.StatusForbidden {
			t.Errorf("POST %s (sans scope) : statut = %d, attendu 403", target, result.Status)
		}
	}
}

// TestPlanningAnonymousIsRedirected : sans session, on part vers /connexion.
func TestPlanningAnonymousIsRedirected(t *testing.T) {
	t.Parallel()

	b := newBrowser(t, newSite(t).handler)

	result := b.get("/planning")
	if result.Status != http.StatusSeeOther || !strings.HasPrefix(result.Location(), "/connexion") {
		t.Errorf("GET /planning : (%d, %q), attendu une redirection vers /connexion", result.Status, result.Location())
	}
}

// TestPlanningJourney est le parcours de référence : trois étapes chaînées, un
// cycle refusé, l'invariant des prérequis au démarrage, les transitions, un
// jalon atteint, et la garde optimiste.
func TestPlanningJourney(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	// Trois étapes : Gros œuvre ← Charpente ← Couverture.
	if result := posterEtape(t, b, "Gros œuvre", "2500-01-01", "2500-01-31"); result.Status != http.StatusSeeOther {
		t.Fatalf("création de l'étape : statut = %d — corps : %s", result.Status, result.Body)
	}
	grosOeuvre := etapeEnregistree(t, s, "Gros œuvre")

	if result := posterEtape(t, b, "Charpente", "2500-02-01", "2500-02-20", grosOeuvre.ID.String()); result.Status != http.StatusSeeOther {
		t.Fatalf("création de la deuxième étape : statut = %d", result.Status)
	}
	charpente := etapeEnregistree(t, s, "Charpente")

	if result := posterEtape(t, b, "Couverture", "2500-02-21", "2500-03-10", charpente.ID.String()); result.Status != http.StatusSeeOther {
		t.Fatalf("création de la troisième étape : statut = %d", result.Status)
	}
	couverture := etapeEnregistree(t, s, "Couverture")

	// Refermer le cycle par la modification du Gros œuvre : refusé en 422.
	result := b.post("/planning/etapes/"+grosOeuvre.ID.String(), url.Values{
		"nom":         {"Gros œuvre"},
		"debut_prevu": {"2500-01-01"},
		"fin_prevue":  {"2500-01-31"},
		"dependances": {couverture.ID.String()},
		"modifie_le":  {modifieLeDe(t, s, "Gros œuvre")},
	})
	if result.Status != http.StatusUnprocessableEntity {
		t.Fatalf("modification cyclique : statut = %d, attendu 422", result.Status)
	}
	if !strings.Contains(result.Body, "formeraient un cycle") {
		t.Errorf("le refus du cycle n'est pas expliqué — corps : %.300s", result.Body)
	}

	// Démarrer la Charpente avant la fin du Gros œuvre : refusé en 422.
	result = transition(t, b, "/planning/etapes/"+charpente.ID.String()+"/demarrer", modifieLeDe(t, s, "Charpente"))
	if result.Status != http.StatusUnprocessableEntity {
		t.Fatalf("démarrage avant prérequis : statut = %d, attendu 422", result.Status)
	}
	if !strings.Contains(result.Body, html.EscapeString("Des prérequis ne sont pas terminés")) {
		t.Errorf("le refus de démarrage n'est pas expliqué — corps : %.300s", result.Body)
	}

	// Le chemin nominal : démarrer et terminer le Gros œuvre, démarrer la
	// Charpente.
	if result := transition(t, b, "/planning/etapes/"+grosOeuvre.ID.String()+"/demarrer", modifieLeDe(t, s, "Gros œuvre")); result.Status != http.StatusSeeOther {
		t.Fatalf("démarrage du Gros œuvre : statut = %d — corps : %s", result.Status, result.Body)
	}
	if result := transition(t, b, "/planning/etapes/"+grosOeuvre.ID.String()+"/terminer", modifieLeDe(t, s, "Gros œuvre")); result.Status != http.StatusSeeOther {
		t.Fatalf("terminaison du Gros œuvre : statut = %d", result.Status)
	}
	if result := transition(t, b, "/planning/etapes/"+charpente.ID.String()+"/demarrer", modifieLeDe(t, s, "Charpente")); result.Status != http.StatusSeeOther {
		t.Fatalf("démarrage de la Charpente : statut = %d — corps : %s", result.Status, result.Body)
	}

	// L'état dérivé s'affiche : terminée, en cours, prévue.
	page := b.get("/planning").Body
	for _, wanted := range []string{"Terminée", "En cours", "Prévue"} {
		if !strings.Contains(page, wanted) {
			t.Errorf("la page ne montre pas le statut %q", wanted)
		}
	}

	// Un jalon, créé puis atteint.
	if result := b.post("/planning/jalons", url.Values{
		"nom":         {"Hors d'eau"},
		"date_prevue": {"2500-03-15"},
	}); result.Status != http.StatusSeeOther {
		t.Fatalf("création du jalon : statut = %d", result.Status)
	}
	jalon, ok := s.planning.jalonParNom("Hors d'eau")
	if !ok {
		t.Fatal("le jalon n'a pas été enregistré")
	}
	if result := transition(t, b, "/planning/jalons/"+jalon.ID.String()+"/atteindre",
		jalon.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")); result.Status != http.StatusSeeOther {
		t.Fatalf("atteinte du jalon : statut = %d — corps : %s", result.Status, result.Body)
	}
	if page := b.get("/planning").Body; !strings.Contains(page, "Atteint") {
		t.Error("la page ne montre pas le jalon atteint")
	}

	// Garde optimiste : une transition soumise avec un modifie_le périmé est
	// redirigée vers l'avis « modifié entre-temps », jamais appliquée.
	stale := transition(t, b, "/planning/etapes/"+charpente.ID.String()+"/terminer", "2001-01-01T00:00:00Z")
	if stale.Status != http.StatusSeeOther || !strings.Contains(stale.Location(), "avis=planning_modifie") {
		t.Fatalf("transition périmée : (%d, %q), attendu la redirection planning_modifie", stale.Status, stale.Location())
	}
	if page := b.get(stale.Location()).Body; !strings.Contains(page, html.EscapeString("modifié cet élément entre-temps")) {
		t.Error("l'avis de modification concurrente ne s'affiche pas")
	}
	if etape := etapeEnregistree(t, s, "Charpente"); etape.Statut() != planning.StatutEnCours {
		t.Errorf("la transition périmée a été appliquée : statut = %q", etape.Statut())
	}
}

// TestPlanningGanttColspans vérifie le rendu du Gantt sur un cas calculé à la
// main : une plage de 30 jours sur 60 colonnes, deux étapes et leurs colspans
// exacts — la preuve que millièmes et colonnes restent en arithmétique
// entière de bout en bout.
func TestPlanningGanttColspans(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	// Plage : du 1ᵉʳ au 30 janvier inclus, soit 30 jours. A couvre les jours
	// 1-10 (le premier tiers), B les jours 11-30 (les deux tiers restants).
	if result := posterEtape(t, b, "Terrassement", "2500-01-01", "2500-01-10"); result.Status != http.StatusSeeOther {
		t.Fatalf("création de A : statut = %d", result.Status)
	}
	if result := posterEtape(t, b, "Fondations", "2500-01-11", "2500-01-30"); result.Status != http.StatusSeeOther {
		t.Fatalf("création de B : statut = %d", result.Status)
	}

	page := b.get("/planning").Body

	// A : 10 jours sur 30 → millièmes [0, 333] → colonnes [0, 19] : une barre
	// de 19 colonnes puis 41 colonnes vides.
	if !strings.Contains(page, `<td colspan="19" class="gantt__barre gantt__barre--prevue"></td>`) {
		t.Error("la barre de Terrassement (colspan 19) est absente")
	}
	if !strings.Contains(page, `<td colspan="41" class="gantt__vide"></td>`) {
		t.Error("le vide après Terrassement (colspan 41) est absent")
	}
	// B : commence au tiers → 19 colonnes vides puis une barre de 41.
	if !strings.Contains(page, `<td colspan="19" class="gantt__vide"></td>`) {
		t.Error("le vide avant Fondations (colspan 19) est absent")
	}
	if !strings.Contains(page, `<td colspan="41" class="gantt__barre gantt__barre--prevue"></td>`) {
		t.Error("la barre de Fondations (colspan 41) est absente")
	}
	// L'axe : quatre graduations de 15 colonnes.
	if got := strings.Count(page, `colspan="15" class="gantt__axe"`); got != 4 {
		t.Errorf("graduations de l'axe : %d, attendu 4", got)
	}
}

// TestPlanningEtapeDevisRetenu : une étape se rattache à un devis RETENU, et
// la page affiche le lot du devis — ou « Devis disparu » quand la référence
// faible ne se résout plus.
func TestPlanningEtapeDevisRetenu(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	// Une consultation avec deux devis reçus : l'un sera retenu, l'autre — le
	// refusé du ricochet — servira de « non retenu ».
	demande := nouvelleDemande(t, s, b)
	for _, entreprise := range []string{entrepriseBas, "Couverture Bresse"} {
		if result := enregistrerDevis(t, b, demande.ID, entreprise, montantBas); result.Status != http.StatusSeeOther {
			t.Fatalf("enregistrement du devis de %s : statut = %d", entreprise, result.Status)
		}
	}
	proposition, _ := s.devis.devisParEntreprise(entrepriseBas)
	if result := b.post("/devis/propositions/"+proposition.ID.String()+"/retenir", url.Values{}); result.Status != http.StatusSeeOther {
		t.Fatalf("rétention du devis : statut = %d", result.Status)
	}
	retenu, _ := s.devis.devisParEntreprise(entrepriseBas)

	// Rattacher une étape au devis retenu : accepté, et le lot s'affiche.
	fields := url.Values{
		"nom":         {"Toiture"},
		"debut_prevu": {"2500-01-01"},
		"fin_prevue":  {"2500-01-20"},
		"devis_id":    {retenu.ID.String()},
	}
	if result := b.post("/planning/etapes", fields); result.Status != http.StatusSeeOther {
		t.Fatalf("création avec devis retenu : statut = %d — corps : %s", result.Status, result.Body)
	}
	if page := b.get("/planning").Body; !strings.Contains(page, entrepriseBas) {
		t.Error("la page n'affiche pas le lot du devis rattaché")
	}

	// Le devis refusé par le ricochet n'est pas retenu : refusé en 422.
	refuse, _ := s.devis.devisParEntreprise("Couverture Bresse")
	fields.Set("nom", "Zinguerie")
	fields.Set("devis_id", refuse.ID.String())
	result := b.post("/planning/etapes", fields)
	if result.Status != http.StatusUnprocessableEntity {
		t.Errorf("création sur un devis non retenu : statut = %d, attendu 422", result.Status)
	}
	if !strings.Contains(result.Body, html.EscapeString("n'est pas retenu")) {
		t.Error("le refus du devis non retenu n'est pas expliqué")
	}
}

// TestPlanningModifierForm : la page de modification arrive pré-remplie, la
// soumission modifie, et une soumission croisée avec une autre modification
// est refusée avec l'avis dédié.
func TestPlanningModifierForm(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	if result := posterEtape(t, b, "Gros œuvre", "2500-01-01", "2500-01-31"); result.Status != http.StatusSeeOther {
		t.Fatalf("création de l'étape : statut = %d", result.Status)
	}
	grosOeuvre := etapeEnregistree(t, s, "Gros œuvre")
	if result := posterEtape(t, b, "Charpente", "2500-02-01", "2500-02-20", grosOeuvre.ID.String()); result.Status != http.StatusSeeOther {
		t.Fatalf("création de la deuxième étape : statut = %d", result.Status)
	}
	charpente := etapeEnregistree(t, s, "Charpente")

	// Le formulaire arrive pré-rempli : nom, dates, prérequis coché, garde.
	page := b.get("/planning/etapes/" + charpente.ID.String() + "/modifier")
	if page.Status != http.StatusOK {
		t.Fatalf("GET modifier : statut = %d", page.Status)
	}
	for _, wanted := range []string{
		`value="Charpente"`,
		`value="2500-02-01"`,
		`value="2500-02-20"`,
		`value="` + grosOeuvre.ID.String() + `"`,
		"checked",
		`name="modifie_le"`,
	} {
		if !strings.Contains(page.Body, wanted) {
			t.Errorf("le formulaire pré-rempli ne contient pas %q", wanted)
		}
	}

	// La soumission modifie le nom et les dates.
	result := b.post("/planning/etapes/"+charpente.ID.String(), url.Values{
		"nom":         {"Charpente et couverture"},
		"debut_prevu": {"2500-02-05"},
		"fin_prevue":  {"2500-02-25"},
		"dependances": {grosOeuvre.ID.String()},
		"modifie_le":  {modifieLeDe(t, s, "Charpente")},
	})
	if result.Status != http.StatusSeeOther {
		t.Fatalf("modification : statut = %d — corps : %s", result.Status, result.Body)
	}
	if _, ok := s.planning.etapeParNom("Charpente et couverture"); !ok {
		t.Error("la modification n'a pas été appliquée")
	}

	// Une garde périmée part en avis « modifié entre-temps ».
	stale := b.post("/planning/etapes/"+charpente.ID.String(), url.Values{
		"nom":         {"Charpente doublée"},
		"debut_prevu": {"2500-02-05"},
		"fin_prevue":  {"2500-02-25"},
		"modifie_le":  {"2001-01-01T00:00:00Z"},
	})
	if stale.Status != http.StatusSeeOther || !strings.Contains(stale.Location(), "avis=planning_modifie") {
		t.Errorf("modification périmée : (%d, %q), attendu la redirection planning_modifie", stale.Status, stale.Location())
	}

	// Une étape inconnue est un 404, pas une panne.
	if result := b.get("/planning/etapes/00000000-0000-4000-8000-00000000dead/modifier"); result.Status != http.StatusNotFound {
		t.Errorf("GET modifier (inconnue) : statut = %d, attendu 404", result.Status)
	}

	// Après un refus sur une étape DÉMARRÉE, le formulaire réaffiché garde les
	// prérequis verrouillés — pas de cases actives que le domaine refuserait.
	if r := transition(t, b, "/planning/etapes/"+grosOeuvre.ID.String()+"/demarrer", modifieLeDe(t, s, "Gros œuvre")); r.Status != http.StatusSeeOther {
		t.Fatalf("démarrage du Gros œuvre : statut = %d", r.Status)
	}
	if r := transition(t, b, "/planning/etapes/"+grosOeuvre.ID.String()+"/terminer", modifieLeDe(t, s, "Gros œuvre")); r.Status != http.StatusSeeOther {
		t.Fatalf("terminaison du Gros œuvre : statut = %d", r.Status)
	}
	if r := transition(t, b, "/planning/etapes/"+charpente.ID.String()+"/demarrer", modifieLeDe(t, s, "Charpente et couverture")); r.Status != http.StatusSeeOther {
		t.Fatalf("démarrage de la Charpente : statut = %d — corps : %s", r.Status, r.Body)
	}
	refused := b.post("/planning/etapes/"+charpente.ID.String(), url.Values{
		"nom":         {"Charpente et couverture"},
		"debut_prevu": {"2500-02-05"},
		"fin_prevue":  {"2500-02-25"},
		// Les prérequis disparaissent de la soumission : refusé, étape démarrée.
		"modifie_le": {modifieLeDe(t, s, "Charpente et couverture")},
	})
	if refused.Status != http.StatusUnprocessableEntity {
		t.Fatalf("retrait des prérequis d'une étape démarrée : statut = %d, attendu 422", refused.Status)
	}
	if !strings.Contains(refused.Body, "disabled") ||
		!strings.Contains(refused.Body, html.EscapeString("ne se modifient plus")) {
		t.Error("le formulaire réaffiché après refus ne verrouille pas les prérequis")
	}
}

// TestPlanningNotFoundBeforeInvalidForm : un élément inconnu est un 404, même
// quand le formulaire soumis est par ailleurs invalide — l'URL prime sur le
// contenu.
func TestPlanningNotFoundBeforeInvalidForm(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	const ghost = "00000000-0000-4000-8000-00000000dead"
	garbage := url.Values{"modifie_le": {"n-importe-quoi"}}

	for _, target := range []string{
		"/planning/etapes/" + ghost,
		"/planning/etapes/" + ghost + "/demarrer",
		"/planning/etapes/" + ghost + "/terminer",
		"/planning/jalons/" + ghost + "/atteindre",
	} {
		if result := b.post(target, garbage); result.Status != http.StatusNotFound {
			t.Errorf("POST %s (inconnu, formulaire invalide) : statut = %d, attendu 404", target, result.Status)
		}
	}
}

// TestPlanningCorruptedGuardIsRejected : un modifie_le forgé est une saisie
// refusée en 422 avec son message — jamais une panne 500.
func TestPlanningCorruptedGuardIsRejected(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	if result := posterEtape(t, b, "Charpente", "2500-01-01", "2500-01-20"); result.Status != http.StatusSeeOther {
		t.Fatalf("création de l'étape : statut = %d", result.Status)
	}
	charpente := etapeEnregistree(t, s, "Charpente")

	// Sur une transition.
	result := transition(t, b, "/planning/etapes/"+charpente.ID.String()+"/demarrer", "n-importe-quoi")
	if result.Status != http.StatusUnprocessableEntity {
		t.Errorf("transition avec garde corrompue : statut = %d, attendu 422", result.Status)
	}
	if !strings.Contains(result.Body, "corrompu") {
		t.Error("le refus de la garde corrompue n'est pas expliqué (transition)")
	}

	// Sur la modification.
	result = b.post("/planning/etapes/"+charpente.ID.String(), url.Values{
		"nom":         {"Charpente"},
		"debut_prevu": {"2500-01-01"},
		"fin_prevue":  {"2500-01-20"},
		"modifie_le":  {"n-importe-quoi"},
	})
	if result.Status != http.StatusUnprocessableEntity {
		t.Errorf("modification avec garde corrompue : statut = %d, attendu 422", result.Status)
	}
	if !strings.Contains(result.Body, "corrompu") {
		t.Error("le refus de la garde corrompue n'est pas expliqué (modification)")
	}

	// Rien n'a bougé.
	if etape := etapeEnregistree(t, s, "Charpente"); etape.Statut() != planning.StatutPrevue {
		t.Errorf("la garde corrompue a laissé passer une écriture : statut = %q", etape.Statut())
	}
}

// TestPlanningPretesADemarrer : la section matérialise les candidates à la
// parallélisation — prévues, tous prérequis terminés.
func TestPlanningPretesADemarrer(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	if result := posterEtape(t, b, "Gros œuvre", "2500-01-01", "2500-01-31"); result.Status != http.StatusSeeOther {
		t.Fatalf("création : statut = %d", result.Status)
	}
	grosOeuvre := etapeEnregistree(t, s, "Gros œuvre")
	if result := posterEtape(t, b, "Charpente", "2500-02-01", "2500-02-20", grosOeuvre.ID.String()); result.Status != http.StatusSeeOther {
		t.Fatalf("création : statut = %d", result.Status)
	}
	if result := posterEtape(t, b, "Clôture", "2500-02-01", "2500-02-10"); result.Status != http.StatusSeeOther {
		t.Fatalf("création : statut = %d", result.Status)
	}

	// Gros œuvre et Clôture sont prêtes (aucun prérequis) ; Charpente attend.
	page := b.get("/planning").Body
	pretes := strings.SplitN(page, "titre-pretes", 2)[1]
	pretes = strings.SplitN(pretes, "titre-etapes", 2)[0]
	if !strings.Contains(pretes, "Gros œuvre") || !strings.Contains(pretes, "Clôture") {
		t.Error("les étapes sans prérequis ne sont pas listées comme prêtes")
	}
	if strings.Contains(pretes, "Charpente") {
		t.Error("Charpente attend son prérequis : elle n'est pas prête")
	}
}
