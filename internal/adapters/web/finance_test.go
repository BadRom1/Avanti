package web_test

import (
	"encoding/csv"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/finance"
)

// retenuPourFinances crée une consultation, y enregistre le devis de référence
// et le retient : c'est le lot engagé auquel les factures et acomptes des
// tests se rattachent. Le montant engagé est celui de montantBas (11 800,50 €,
// soit 1 180 050 centimes).
func retenuPourFinances(t *testing.T, s *site, b *browser) devis.Devis {
	t.Helper()

	demande := nouvelleDemande(t, s, b)
	if result := enregistrerDevis(t, b, demande.ID, entrepriseBas, montantBas); result.Status != http.StatusSeeOther {
		t.Fatalf("enregistrement du devis : statut = %d", result.Status)
	}

	proposition, ok := s.devis.devisParEntreprise(entrepriseBas)
	if !ok {
		t.Fatal("le devis n'a pas été enregistré")
	}

	if result := b.post("/devis/propositions/"+proposition.ID.String()+"/retenir", url.Values{}); result.Status != http.StatusSeeOther {
		t.Fatalf("rétention du devis : statut = %d — corps : %s", result.Status, result.Body)
	}

	retenu, _ := s.devis.devisParEntreprise(entrepriseBas)
	if retenu.Statut != devis.StatutRetenu {
		t.Fatalf("statut du devis = %q, attendu retenu", retenu.Statut)
	}

	return retenu
}

// posterFacture soumet le formulaire de facture.
func posterFacture(t *testing.T, b *browser, devisID, entreprise, montant string) httpResult {
	t.Helper()

	return b.post("/finances/factures", url.Values{
		"devis_id":   {devisID},
		"entreprise": {entreprise},
		"montant":    {montant},
		"date_piece": {"2026-04-03"},
		"numero":     {"F-2026-042"},
	})
}

// posterAcompte soumet le formulaire d'acompte.
func posterAcompte(t *testing.T, b *browser, devisID, entreprise, montant string) httpResult {
	t.Helper()

	return b.post("/finances/acomptes", url.Values{
		"devis_id":   {devisID},
		"entreprise": {entreprise},
		"montant":    {montant},
		"date_piece": {"2026-04-10"},
		"moyen":      {"virement"},
	})
}

// TestFinanceRoutesRequireScope est la vérification qui compte le plus de ce
// lot : TOUT /finances est gardé par un scope, export compris, et le
// collaborateur — dont le rôle ne porte aucun scope finance — est refusé
// partout.
func TestFinanceRoutesRequireScope(t *testing.T) {
	t.Parallel()

	s := newSite(t)

	for _, email := range []string{collaboratorEmail, addAccountWithoutScopes(t, s)} {
		b := newBrowser(t, s.handler)
		b.login(email)

		reads := []string{"/finances", "/finances/export/csv", "/finances/export/pdf"}
		for _, target := range reads {
			if result := b.get(target); result.Status != http.StatusForbidden {
				t.Errorf("GET %s (%s) : statut = %d, attendu 403", target, email, result.Status)
			}
		}

		writes := []string{
			"/finances/factures",
			"/finances/acomptes",
			"/finances/factures/peu-importe/payer",
			"/finances/factures/peu-importe/assurance/envoyer",
			"/finances/factures/peu-importe/assurance/rembourser",
			"/finances/acomptes/peu-importe/assurance/envoyer",
			"/finances/acomptes/peu-importe/assurance/rembourser",
		}
		for _, target := range writes {
			if result := b.post(target, url.Values{}); result.Status != http.StatusForbidden {
				t.Errorf("POST %s (%s) : statut = %d, attendu 403", target, email, result.Status)
			}
		}
	}
}

// TestFinanceAnonymousIsRedirected : sans session, on part vers /connexion —
// jamais un dossier financier sans authentification.
func TestFinanceAnonymousIsRedirected(t *testing.T) {
	t.Parallel()

	b := newBrowser(t, newSite(t).handler)

	for _, target := range []string{"/finances", "/finances/export/csv"} {
		result := b.get(target)
		if result.Status != http.StatusSeeOther || !strings.HasPrefix(result.Location(), "/connexion") {
			t.Errorf("GET %s : (%d, %q), attendu une redirection vers /connexion", target, result.Status, result.Location())
		}
	}
}

// TestFinanceJourney est le parcours de référence : un devis retenu, une
// facture, des acomptes sous l'invariant, la synthèse qui rapproche le tout.
func TestFinanceJourney(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)
	retenuPourFinances(t, s, b)

	// La page vierge annonce le lot engagé, avec son montant.
	page := b.get("/finances")
	if page.Status != http.StatusOK {
		t.Fatalf("GET /finances : statut = %d", page.Status)
	}
	if !strings.Contains(page.Body, lotTest+" — "+entrepriseBas) {
		t.Error("la synthèse n'annonce pas le lot engagé")
	}
	if !strings.Contains(page.Body, montantBasAffiche) {
		t.Error("la synthèse n'affiche pas le montant engagé")
	}

	retenu, _ := s.devis.devisParEntreprise(entrepriseBas)
	devisID := retenu.ID.String()

	// Une facture rattachée au lot.
	result := posterFacture(t, b, devisID, entrepriseBas, "2 000,00")
	if result.Status != http.StatusSeeOther {
		t.Fatalf("facture : statut = %d — corps : %s", result.Status, result.Body)
	}
	page = b.get(result.Location())
	if !strings.Contains(page.Body, "La facture a été enregistrée.") {
		t.Error("l'avis d'enregistrement de la facture manque")
	}
	if !strings.Contains(page.Body, "F-2026-042") {
		t.Error("la facture n'apparaît pas dans la liste")
	}

	// Un premier acompte : 10 000,00 € sur 11 800,50 engagés.
	if premier := posterAcompte(t, b, devisID, entrepriseBas, "10 000,00"); premier.Status != http.StatusSeeOther {
		t.Fatalf("premier acompte : statut = %d — corps : %s", premier.Status, premier.Body)
	}

	// 2 000,00 de plus dépasseraient : refusé, formulaire réaffiché en 422,
	// et rien n'est écrit.
	result = posterAcompte(t, b, devisID, entrepriseBas, "2000,00")
	if result.Status != http.StatusUnprocessableEntity {
		t.Fatalf("acompte au-delà de l'engagé : statut = %d, attendu 422", result.Status)
	}
	if !strings.Contains(result.Body, "Le cumul des acomptes dépasserait le montant engagé sur ce devis.") {
		t.Error("le message de dépassement manque")
	}
	if cumul, sumErr := s.finance.SumAcomptesByDevis(t.Context(), devisID); sumErr != nil || cumul != 1_000_000 {
		t.Errorf("cumul = (%d, %v), le refus a laissé une écriture", int64(cumul), sumErr)
	}

	// 1 800,50 soldent exactement l'engagement : accepté.
	if solde := posterAcompte(t, b, devisID, entrepriseBas, "1800,50"); solde.Status != http.StatusSeeOther {
		t.Fatalf("acompte de solde : statut = %d — corps : %s", solde.Status, solde.Body)
	}

	// Hors devis : pas d'engagement, le montant est libre.
	if libre := posterAcompte(t, b, "", "Négoce Matériaux", "50 000,00"); libre.Status != http.StatusSeeOther {
		t.Fatalf("acompte hors devis : statut = %d — corps : %s", libre.Status, libre.Body)
	}

	// La synthèse rapproche, colonne par colonne — les assertions sont
	// ancrées sur la ligne, pas sur la page entière où « 0,00 » se trouve
	// partout.
	page = b.get("/finances")
	synthese := sectionOf(t, page.Body, "titre-synthese", "titre-factures")

	// Le lot : 11 800,50 engagés, 2 000,00 facturés, 11 800,50 payés (les
	// deux acomptes), rien de remboursé, reste à payer soldé.
	lot := tableRowOf(t, synthese, lotTest+" — "+entrepriseBas)
	wantInOrder(t, "ligne du lot", lot,
		"11 800,50", "2 000,00", "11 800,50", "0,00", "0,00")

	// Hors devis : rien d'engagé ni de facturé, 50 000,00 payés.
	hors := tableRowOf(t, synthese, "Hors devis")
	wantInOrder(t, "ligne hors devis", hors, "0,00", "0,00", "50 000,00", "0,00")

	// Total chantier : le reste à payer est négatif — payé au-delà de
	// l'engagé par le versement hors devis — et la synthèse le montre tel
	// quel, c'est assumé.
	total := tableRowOf(t, synthese, "Total chantier")
	wantInOrder(t, "ligne total", total,
		"11 800,50", "2 000,00", "61 800,50", "0,00", "-50 000,00")
}

// sectionOf extrait la portion de page entre deux ancres, pour ancrer les
// assertions sur la bonne table.
func sectionOf(t *testing.T, body, from, to string) string {
	t.Helper()

	start := strings.Index(body, from)
	if start < 0 {
		t.Fatalf("l'ancre %q est introuvable dans la page", from)
	}
	rest := body[start:]

	end := strings.Index(rest, to)
	if end < 0 {
		t.Fatalf("l'ancre %q est introuvable après %q", to, from)
	}

	return rest[:end]
}

// tableRowOf extrait la ligne de tableau qui porte le libellé donné.
func tableRowOf(t *testing.T, section, libelle string) string {
	t.Helper()

	start := strings.Index(section, libelle)
	if start < 0 {
		t.Fatalf("la ligne %q est introuvable", libelle)
	}
	rest := section[start:]

	end := strings.Index(rest, "</tr>")
	if end < 0 {
		t.Fatalf("la ligne %q ne se referme pas", libelle)
	}

	return rest[:end]
}

// wantInOrder vérifie que les valeurs apparaissent dans la ligne, dans cet
// ordre et comme cellules entières (>valeur<) — c'est ce qui distingue la
// colonne « payé » de la colonne « remboursé » quand toutes deux affichent
// 0,00.
func wantInOrder(t *testing.T, label, row string, values ...string) {
	t.Helper()

	rest := row
	for _, value := range values {
		// La cellule rendue est « >11 800,50 €< » : le montant et son unité,
		// posée par le catalogue (devis.montant).
		index := strings.Index(rest, ">"+value+" €<")
		if index < 0 {
			t.Fatalf("%s : la valeur %q manque (ou pas dans l'ordre) — ligne : %s", label, value, row)
		}
		rest = rest[index+1:]
	}
}

// TestFinanceTransitions joue les cycles de paiement et d'assurance depuis
// l'interface, refus compris.
func TestFinanceTransitions(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	if result := posterFacture(t, b, "", "Négoce Matériaux", "400,00"); result.Status != http.StatusSeeOther {
		t.Fatalf("facture : statut = %d", result.Status)
	}
	facture, ok := s.finance.factureParEntreprise("Négoce Matériaux")
	if !ok {
		t.Fatal("la facture n'a pas été enregistrée")
	}
	base := "/finances/factures/" + facture.ID.String()

	// Payer, puis re-payer : le second clic est refusé avec un message, et la
	// page réaffichée porte l'état réel.
	result := b.post(base+"/payer", url.Values{})
	if result.Status != http.StatusSeeOther {
		t.Fatalf("payer : statut = %d — corps : %s", result.Status, result.Body)
	}
	if page := b.get(result.Location()); !strings.Contains(page.Body, "La facture a été marquée payée.") {
		t.Error("l'avis de paiement manque")
	}

	result = b.post(base+"/payer", url.Values{})
	if result.Status != http.StatusUnprocessableEntity {
		t.Fatalf("second paiement : statut = %d, attendu 422", result.Status)
	}
	if !strings.Contains(result.Body, "Cette facture est déjà payée.") {
		t.Error("le message de double paiement manque")
	}

	// Rembourser avant d'envoyer : le cycle assurance l'interdit.
	result = b.post(base+"/assurance/rembourser", url.Values{"montant_rembourse": {"100,00"}})
	if result.Status != http.StatusUnprocessableEntity {
		t.Fatalf("remboursement avant envoi : statut = %d, attendu 422", result.Status)
	}

	// Envoyer, puis rembourser trop, puis rembourser juste.
	if envoi := b.post(base+"/assurance/envoyer", url.Values{}); envoi.Status != http.StatusSeeOther {
		t.Fatalf("envoi assurance : statut = %d", envoi.Status)
	}
	result = b.post(base+"/assurance/rembourser", url.Values{"montant_rembourse": {"500,00"}})
	if result.Status != http.StatusUnprocessableEntity {
		t.Fatalf("remboursement au-delà de la pièce : statut = %d, attendu 422", result.Status)
	}
	if !strings.Contains(result.Body, "Le montant remboursé doit être positif") {
		t.Error("le message de remboursement invalide manque")
	}

	result = b.post(base+"/assurance/rembourser", url.Values{"montant_rembourse": {"400,00"}})
	if result.Status != http.StatusSeeOther {
		t.Fatalf("remboursement : statut = %d — corps : %s", result.Status, result.Body)
	}
	page := b.get(result.Location())
	if !strings.Contains(page.Body, "Le remboursement a été enregistré.") {
		t.Error("l'avis de remboursement manque")
	}
	if !strings.Contains(page.Body, "Remboursée") {
		t.Error("le statut remboursé n'apparaît pas")
	}

	// Le cycle des acomptes, avec ses propres routes.
	if depot := posterAcompte(t, b, "", "Négoce Matériaux", "150,00"); depot.Status != http.StatusSeeOther {
		t.Fatalf("acompte : statut = %d", depot.Status)
	}
	acompte, ok := s.finance.acompteParEntreprise("Négoce Matériaux")
	if !ok {
		t.Fatal("l'acompte n'a pas été enregistré")
	}
	acompteBase := "/finances/acomptes/" + acompte.ID.String()

	if result := b.post(acompteBase+"/assurance/envoyer", url.Values{}); result.Status != http.StatusSeeOther {
		t.Fatalf("envoi assurance de l'acompte : statut = %d", result.Status)
	}
	if result := b.post(acompteBase+"/assurance/rembourser", url.Values{"montant_rembourse": {"150,00"}}); result.Status != http.StatusSeeOther {
		t.Fatalf("remboursement de l'acompte : statut = %d — corps : %s", result.Status, result.Body)
	}

	stored, _ := s.finance.acompteParEntreprise("Négoce Matériaux")
	if stored.Assurance.Statut != finance.AssuranceRemboursee || stored.Assurance.MontantRembourse != 15_000 {
		t.Errorf("acompte relu = %+v", stored.Assurance)
	}
}

// TestFinanceTransitionsUnknownPiece : une pièce inconnue est une page
// introuvable, pas une panne.
func TestFinanceTransitionsUnknownPiece(t *testing.T) {
	t.Parallel()

	b := newBrowser(t, newSite(t).handler)
	b.login(ownerEmail)

	targets := []string{
		"/finances/factures/6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5/payer",
		"/finances/factures/6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5/assurance/envoyer",
		"/finances/acomptes/6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5/assurance/envoyer",
	}
	for _, target := range targets {
		if result := b.post(target, url.Values{}); result.Status != http.StatusNotFound {
			t.Errorf("POST %s : statut = %d, attendu 404", target, result.Status)
		}
	}
}

// TestFinanceRejectsDevisNonRetenu : une facture comme un acompte se rattachent
// à un lot ENGAGÉ. Un devis encore en comparaison — ou inconnu — est refusé
// avec un message, jamais accepté en silence.
func TestFinanceRejectsDevisNonRetenu(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	// Un devis reçu, jamais retenu.
	demande := nouvelleDemande(t, s, b)
	if result := enregistrerDevis(t, b, demande.ID, entrepriseHaut, montantHaut); result.Status != http.StatusSeeOther {
		t.Fatalf("enregistrement du devis : statut = %d", result.Status)
	}
	recu, _ := s.devis.devisParEntreprise(entrepriseHaut)

	for name, post := range map[string]func() httpResult{
		"facture": func() httpResult { return posterFacture(t, b, recu.ID.String(), entrepriseHaut, "100,00") },
		"acompte": func() httpResult { return posterAcompte(t, b, recu.ID.String(), entrepriseHaut, "100,00") },
	} {
		result := post()
		if result.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s sur un devis non retenu : statut = %d, attendu 422", name, result.Status)
		}
		// Le corps est du HTML : l'apostrophe du message y est échappée.
		if !strings.Contains(result.Body, html.EscapeString("Ce devis n'est pas retenu")) {
			t.Errorf("%s : le message de refus manque", name)
		}
	}

	// Un devis qui n'existe pas du tout.
	result := posterFacture(t, b, "6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5", entrepriseHaut, "100,00")
	if result.Status != http.StatusUnprocessableEntity || !strings.Contains(result.Body, html.EscapeString("Le devis choisi n'existe pas.")) {
		t.Errorf("facture sur un devis inconnu : statut = %d", result.Status)
	}

	if factures, listErr := s.finance.ListFactures(t.Context()); listErr != nil || len(factures) != 0 {
		t.Errorf("un refus a laissé une facture derrière lui : (%d, %v)", len(factures), listErr)
	}
}

// TestFinanceFormRedisplay : une saisie refusée réaffiche le formulaire soumis,
// en 422, avec le message et les valeurs déjà tapées — rien à retaper.
func TestFinanceFormRedisplay(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	result := b.post("/finances/factures", url.Values{
		"devis_id":   {""},
		"entreprise": {"Négoce Matériaux"},
		"montant":    {""},
		"date_piece": {"2026-04-03"},
	})
	if result.Status != http.StatusUnprocessableEntity {
		t.Fatalf("statut = %d, attendu 422", result.Status)
	}
	if !strings.Contains(result.Body, "Le montant est obligatoire.") {
		t.Error("le message de montant vide manque")
	}
	if !strings.Contains(result.Body, `value="Négoce Matériaux"`) {
		t.Error("l'entreprise saisie n'est pas réaffichée")
	}

	// Un moyen de paiement inventé sur l'acompte.
	result = b.post("/finances/acomptes", url.Values{
		"devis_id":   {""},
		"entreprise": {"Négoce Matériaux"},
		"montant":    {"100,00"},
		"date_piece": {"2026-04-03"},
		"moyen":      {"troc"},
	})
	if result.Status != http.StatusUnprocessableEntity || !strings.Contains(result.Body, "Choisissez un moyen de paiement") {
		t.Errorf("moyen inventé : statut = %d", result.Status)
	}
}

// TestFactureJustificatifs : le dépôt d'un justificatif rattaché à une facture
// ramène à la page des finances, la pièce s'affiche sous sa facture avec son
// lien de téléchargement, et une cible de facture inexistante est refusée.
func TestFactureJustificatifs(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	if result := posterFacture(t, b, "", "Négoce Matériaux", "400,00"); result.Status != http.StatusSeeOther {
		t.Fatalf("facture : statut = %d", result.Status)
	}
	facture, ok := s.finance.factureParEntreprise("Négoce Matériaux")
	if !ok {
		t.Fatal("la facture n'a pas été enregistrée")
	}

	// Le dépôt heureux ramène aux finances, avec l'avis.
	result := postUpload(t, b, map[string]string{
		"categorie":  "facture",
		"cible_type": "facture",
		"cible_id":   facture.ID.String(),
	}, uploadField{name: "justificatif.pdf", content: []byte(pdfContent), contentType: "application/pdf"})
	if result.Status != http.StatusSeeOther {
		t.Fatalf("dépôt : statut = %d — corps : %s", result.Status, result.Body)
	}
	if result.Location() != "/finances?avis=document_ajoute" {
		t.Errorf("redirection vers %q, attendu /finances?avis=document_ajoute", result.Location())
	}

	page := b.get(result.Location())
	if !strings.Contains(page.Body, "La pièce a été déposée.") {
		t.Error("l'avis de dépôt manque")
	}
	if !strings.Contains(page.Body, "justificatif.pdf") {
		t.Error("le justificatif n'est pas listé sous la facture")
	}
	if !strings.Contains(page.Body, "/telecharger") {
		t.Error("le lien de téléchargement du justificatif manque")
	}
	if !strings.Contains(page.Body, "Facture concernée") {
		t.Error("le formulaire de dépôt de justificatif manque")
	}

	// Une cible de facture inexistante est refusée avec un message ; rien
	// n'est déposé.
	result = postUpload(t, b, map[string]string{
		"categorie":  "facture",
		"cible_type": "facture",
		"cible_id":   "6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5",
	}, uploadField{name: "fantome.pdf", content: []byte(pdfContent), contentType: "application/pdf"})
	if result.Status != http.StatusUnprocessableEntity {
		t.Fatalf("cible de facture inconnue : statut = %d, attendu 422", result.Status)
	}
	if !strings.Contains(result.Body, html.EscapeString("La facture à laquelle rattacher cette pièce n'existe pas.")) {
		t.Error("le message de cible inconnue manque")
	}
	if _, deposee := s.documents.documentParNom("fantome.pdf"); deposee {
		t.Error("le refus a laissé une pièce derrière lui")
	}
}

// TestFinanceExport : le dossier se télécharge, en pièce jointe, dans le format
// demandé — CSV relu par encoding/csv, PDF reconnu à son entête — et son
// contenu est celui que l'adapter a assemblé : libellés de devis et
// justificatifs compris.
func TestFinanceExport(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)
	retenuPourFinances(t, s, b)

	retenu, _ := s.devis.devisParEntreprise(entrepriseBas)
	if result := posterFacture(t, b, retenu.ID.String(), entrepriseBas, "2 000,00"); result.Status != http.StatusSeeOther {
		t.Fatalf("facture : statut = %d", result.Status)
	}
	facture, _ := s.finance.factureParEntreprise(entrepriseBas)

	// Un justificatif rattaché à la facture, par la cible facture/{id} du
	// domaine document.
	deposerPiece(t, s, b, map[string]string{
		"categorie":  "facture",
		"cible_type": "facture",
		"cible_id":   facture.ID.String(),
	})

	// CSV : en-têtes de téléchargement puis contenu relu.
	result := b.get("/finances/export/csv")
	if result.Status != http.StatusOK {
		t.Fatalf("export CSV : statut = %d — corps : %s", result.Status, result.Body)
	}
	if got := result.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Errorf("Content-Type = %q", got)
	}
	disposition := result.Header.Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment") || !strings.Contains(disposition, ".csv") {
		t.Errorf("Content-Disposition = %q", disposition)
	}

	rows, err := csv.NewReader(strings.NewReader(result.Body)).ReadAll()
	if err != nil {
		t.Fatalf("le CSV téléchargé ne se relit pas : %v", err)
	}
	var factureRow []string
	for _, row := range rows {
		if row[0] == "Facture" {
			factureRow = row
		}
	}
	if factureRow == nil {
		t.Fatalf("aucune ligne de facture dans le CSV : %v", rows)
	}
	if factureRow[1] != lotTest+" — "+entrepriseBas {
		t.Errorf("libellé du devis = %q", factureRow[1])
	}
	if !strings.Contains(factureRow[4], "devis-charpente.pdf") {
		t.Errorf("le justificatif manque : %q", factureRow[4])
	}

	// PDF : l'entête fait foi.
	result = b.get("/finances/export/pdf")
	if result.Status != http.StatusOK {
		t.Fatalf("export PDF : statut = %d", result.Status)
	}
	if got := result.Header.Get("Content-Type"); got != "application/pdf" {
		t.Errorf("Content-Type = %q", got)
	}
	if !strings.HasPrefix(result.Body, "%PDF-") {
		t.Errorf("le PDF ne commence pas par %%PDF- : %q", result.Body[:min(len(result.Body), 16)])
	}

	// Un format inconnu est une page introuvable.
	if result := b.get("/finances/export/xml"); result.Status != http.StatusNotFound {
		t.Errorf("export xml : statut = %d, attendu 404", result.Status)
	}
}
