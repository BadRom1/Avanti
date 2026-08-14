// Test d'intégration de `avanti seed demo`, joué par la vraie porte : la
// commande run() entière, configuration par l'environnement, migrations et
// services de domaine compris, contre un PostgreSQL réel.
//
// Il vit dans cmd/avanti parce que le seed y vit : c'est le seul endroit du
// dépôt autorisé à assembler les domaines (R4 de docs/ARCHITECTURE.md), et ce
// test vérifie exactement cet assemblage — les invariants inter-domaines du
// jeu de démonstration, et les garde-fous qui empêchent le seed de toucher une
// base qui a vécu.
//
// Pas de t.Parallel() : t.Setenv configure le processus entier, ce que le
// parallélisme interdirait.
package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/adapters/postgres"
	"github.com/Romain-Badino/Avanti/internal/adapters/storage"
	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/finance"
	"github.com/Romain-Badino/Avanti/internal/planning"
)

const seedEmail = "romain@exemple.fr"

func TestSeedDemoEndToEnd(t *testing.T) {
	dsn := freshDatabase(t)

	t.Setenv("AVANTI_ENV", "development")
	t.Setenv("AVANTI_DATABASE_URL", dsn)
	t.Setenv("AVANTI_OAUTH_SECRET", strings.Repeat("k", 44))
	t.Setenv("AVANTI_DOCUMENTS_DIR", t.TempDir())

	// 1. Sur une base vide, le seed refuse un compte inconnu : il attribue ses
	//    saisies, il ne crée jamais de compte.
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), []string{"seed", "demo", "--email", seedEmail}, &stdout, &stderr)
	if err == nil {
		t.Fatal("seed demo doit refuser un compte inconnu")
	}
	if !strings.Contains(err.Error(), "user add") {
		t.Errorf("erreur = %q, doit orienter vers « avanti user add »", err.Error())
	}

	// 2. Le compte créé (par la CLI réelle), le seed passe.
	stdout.Reset()
	stderr.Reset()
	if addErr := run(t.Context(), []string{
		"user", "add", "--email", seedEmail, "--nom", "Romain", "--role", "proprietaire", "--generate",
	}, &stdout, &stderr); addErr != nil {
		t.Fatalf("création du compte : %v — stderr : %s", addErr, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if seedErr := run(t.Context(), []string{"seed", "demo", "--email", seedEmail}, &stdout, &stderr); seedErr != nil {
		t.Fatalf("seed demo échoué : %v — stderr : %s", seedErr, stderr.String())
	}
	for _, want := range []string{"Devis", "Finances", "Planning", "Documents", seedEmail} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("sortie du seed sans %q :\n%s", want, stdout.String())
		}
	}

	// 3. Les invariants du jeu créé, relus par les services de domaine.
	pool := openPool(t, dsn)

	devisService, err := newDevisService(pool)
	if err != nil {
		t.Fatalf("newDevisService() échoué : %v", err)
	}

	comparaisons, err := devisService.Comparaisons(t.Context())
	if err != nil {
		t.Fatalf("lecture des comparaisons : %v", err)
	}
	if len(comparaisons) != 3 {
		t.Fatalf("comparaisons = %d, attendu 3", len(comparaisons))
	}

	var retainedID string
	closed, open := 0, 0
	for _, comparaison := range comparaisons {
		if comparaison.Closed() {
			closed++
			retained, _ := comparaison.Retenu()
			retainedID = retained.ID.String()
			if retained.Artisan.Entreprise != "Charpentes Morel" {
				t.Errorf("devis retenu par %q, attendu Charpentes Morel", retained.Artisan.Entreprise)
			}
		} else {
			open++
		}
	}
	if closed != 1 || open != 2 {
		t.Errorf("demandes tranchées = %d, ouvertes = %d — attendu 1 et 2", closed, open)
	}

	// Finances : trois factures aux trois stades du suivi, un acompte sous
	// l'engagement.
	financeService, err := newFinanceService(pool)
	if err != nil {
		t.Fatalf("newFinanceService() échoué : %v", err)
	}

	factures, err := financeService.Factures(t.Context())
	if err != nil {
		t.Fatalf("lecture des factures : %v", err)
	}
	if len(factures) != 3 {
		t.Fatalf("factures = %d, attendu 3", len(factures))
	}
	statuts := map[finance.StatutAssurance]int{}
	payees := 0
	for _, facture := range factures {
		statuts[facture.Assurance.Statut]++
		if facture.Paiement == finance.PaiementPayee {
			payees++
		}
	}
	if payees != 1 {
		t.Errorf("factures payées = %d, attendu 1", payees)
	}
	for statut, want := range map[finance.StatutAssurance]int{
		finance.AssuranceNonEnvoyee: 1,
		finance.AssuranceEnvoyee:    1,
		finance.AssuranceRemboursee: 1,
	} {
		if statuts[statut] != want {
			t.Errorf("factures %s = %d, attendu %d", statut, statuts[statut], want)
		}
	}

	acomptes, err := financeService.Acomptes(t.Context())
	if err != nil {
		t.Fatalf("lecture des acomptes : %v", err)
	}
	if len(acomptes) != 1 {
		t.Fatalf("acomptes = %d, attendu 1", len(acomptes))
	}
	if acomptes[0].DevisID != retainedID {
		t.Errorf("acompte rattaché à %q, attendu le devis retenu %q", acomptes[0].DevisID, retainedID)
	}

	// Planning : les quatre statuts dérivés et les deux jalons.
	planningService, err := newPlanningService(pool)
	if err != nil {
		t.Fatalf("newPlanningService() échoué : %v", err)
	}

	etapes, err := planningService.Etapes(t.Context())
	if err != nil {
		t.Fatalf("lecture des étapes : %v", err)
	}
	if len(etapes) != 4 {
		t.Fatalf("étapes = %d, attendu 4", len(etapes))
	}
	now := time.Now().UTC()
	byName := map[string]planning.Etape{}
	for _, etape := range etapes {
		byName[etape.Name] = etape
	}
	if statut := byName["Démolition et curage"].Statut(); statut != planning.StatutTerminee {
		t.Errorf("démolition : statut = %s, attendu terminée", statut)
	}
	if statut := byName["Charpente"].Statut(); statut != planning.StatutEnCours {
		t.Errorf("charpente : statut = %s, attendu en cours", statut)
	}
	if couverture := byName["Couverture"]; couverture.Statut() != planning.StatutPrevue || !couverture.EnRetard(now) {
		t.Errorf("couverture : statut = %s, en retard = %t — attendu prévue et en retard",
			couverture.Statut(), couverture.EnRetard(now))
	}
	electricite := byName["Électricité"]
	if electricite.Statut() != planning.StatutPrevue || electricite.EnRetard(now) {
		t.Errorf("électricité : statut = %s, en retard = %t — attendu prévue, sans retard",
			electricite.Statut(), electricite.EnRetard(now))
	}
	if len(electricite.DependsOn) != 1 || byName["Démolition et curage"].ID != electricite.DependsOn[0] {
		t.Errorf("électricité : prérequis = %v, attendu la seule démolition", electricite.DependsOn)
	}

	jalons, err := planningService.Jalons(t.Context())
	if err != nil {
		t.Fatalf("lecture des jalons : %v", err)
	}
	if len(jalons) != 2 {
		t.Fatalf("jalons = %d, attendu 2", len(jalons))
	}
	atteints := 0
	for _, jalon := range jalons {
		if jalon.Atteint() {
			atteints++
		}
	}
	if atteints != 1 {
		t.Errorf("jalons atteints = %d, attendu 1", atteints)
	}

	// Documents : deux PDF rattachés, dont le contenu se relit vraiment.
	checkSeededDocuments(t, dsn, retainedID)

	// 4. Rejouer le seed est refusé : la base a vécu.
	stdout.Reset()
	stderr.Reset()
	err = run(t.Context(), []string{"seed", "demo", "--email", seedEmail}, &stdout, &stderr)
	if err == nil {
		t.Fatal("un second seed sur la même base doit être refusé")
	}
	if !strings.Contains(err.Error(), "vide") {
		t.Errorf("erreur = %q, doit dire que le seed exige une instance vide", err.Error())
	}
}

// checkSeededDocuments relit les pièces déposées par le seed : rattachements
// et contenu binaire — un PDF qui commence bien par sa signature.
func checkSeededDocuments(t *testing.T, dsn, retainedID string) {
	t.Helper()

	pool := openPool(t, dsn)

	// Le service se monte sur le MÊME répertoire que celui que le seed a reçu
	// par l'environnement : c'est là que les contenus ont été écrits.
	documentRepo, err := postgres.NewDocumentRepo(pool)
	if err != nil {
		t.Fatalf("postgres.NewDocumentRepo() échoué : %v", err)
	}
	contentStorage, err := storage.NewFilesystem(os.Getenv("AVANTI_DOCUMENTS_DIR"))
	if err != nil {
		t.Fatalf("storage.NewFilesystem() échoué : %v", err)
	}
	documentsService, err := document.NewService(document.ServiceOptions{
		Repo:    documentRepo,
		Storage: contentStorage,
	})
	if err != nil {
		t.Fatalf("document.NewService() échoué : %v", err)
	}

	docs, err := documentsService.Documents(t.Context())
	if err != nil {
		t.Fatalf("lecture des documents : %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("documents = %d, attendu 2", len(docs))
	}

	attached, err := documentsService.DocumentsByTarget(t.Context(),
		document.Target{Type: document.TargetDevis, ID: retainedID})
	if err != nil {
		t.Fatalf("lecture des pièces du devis retenu : %v", err)
	}
	if len(attached) != 1 {
		t.Fatalf("pièces du devis retenu = %d, attendu 1", len(attached))
	}

	doc, content, err := documentsService.Open(t.Context(), attached[0].ID)
	if err != nil {
		t.Fatalf("ouverture de la pièce %s : %v", attached[0].ID, err)
	}
	defer func() {
		if closeErr := content.Close(); closeErr != nil {
			t.Errorf("fermeture du contenu : %v", closeErr)
		}
	}()

	header := make([]byte, 5)
	if _, err := content.Read(header); err != nil {
		t.Fatalf("lecture du contenu : %v", err)
	}
	if string(header) != "%PDF-" {
		t.Errorf("contenu = %q…, attendu une signature PDF", header)
	}
	if doc.MimeType != "application/pdf" || doc.SizeBytes < 500 {
		t.Errorf("métadonnées : type = %s, taille = %d — attendu un PDF d'au moins 500 octets",
			doc.MimeType, doc.SizeBytes)
	}
}
