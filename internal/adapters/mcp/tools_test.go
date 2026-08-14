package mcp_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/planning"
)

// decodeStructured relit la sortie structurée d'un tool dans la forme donnée.
func decodeStructured(t *testing.T, result *sdk.CallToolResult, out any) {
	t.Helper()

	if result.IsError {
		t.Fatalf("le tool a rendu une erreur : %s", resultText(result))
	}

	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("sortie structurée non sérialisable : %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("sortie structurée illisible : %v — %s", err, raw)
	}
}

// TestToolsListed vérifie que la session s'ouvre avec un jeton de propriétaire
// et que les tools annoncés portent des noms français.
func TestToolsListed(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	session := tb.connect(t, tokenOwner)

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list : %v", err)
	}

	names := make(map[string]bool, len(tools.Tools))
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}

	for _, want := range []string{
		"devis_liste", "devis_detail", "devis_enregistrer",
		"finances_synthese", "finances_factures", "finances_acomptes",
		"facture_enregistrer", "acompte_enregistrer",
		"planning_etapes", "planning_jalons", "etape_demarrer", "etape_terminer",
		"documents_liste", "assurance_preparer_envoi",
	} {
		if !names[want] {
			t.Errorf("tool %q absent de tools/list", want)
		}
	}
}

// TestToolRefusedWithoutDomainScope vérifie le refus par scope de domaine : un
// jeton qui porte mcp mais pas devis:read reçoit une erreur qui NOMME le scope,
// jamais un résultat vide.
//
// L'acteur est FORGÉ (identity.NewActorWithScopes) : aucun rôle réel ne porte
// mcp sans les scopes de domaine — le propriétaire a tout, le collaborateur n'a
// pas mcp — et un jeton réel ne peut donc pas produire ce cas aujourd'hui. Le
// forger reste nécessaire : le consentement OAuth permet de n'accorder qu'une
// partie des scopes demandés, et ce refus est ce qu'un agent verrait alors.
func TestToolRefusedWithoutDomainScope(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	tb.seedComparaison(t)

	session := tb.connect(t, tokenMCPOnly)

	result := callTool(t, session, "devis_liste", nil)

	if !result.IsError {
		t.Fatal("le tool a répondu à un jeton sans devis:read")
	}
	if text := resultText(result); !strings.Contains(text, "devis:read") {
		t.Errorf("le refus ne nomme pas le scope manquant : %q", text)
	}
}

// TestDevisListeEtEnregistrer joue une consultation puis une écriture avec un
// jeton de propriétaire : la liste rend le semé, l'écriture atteint le domaine
// avec l'identifiant du compte en acteur.
func TestDevisListeEtEnregistrer(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	demande, seeded := tb.seedComparaison(t)

	session := tb.connect(t, tokenOwner)

	var liste struct {
		Comparaisons []struct {
			Demande struct {
				ID  string `json:"id"`
				Lot string `json:"lot"`
			} `json:"demande"`
			Devis []struct {
				ID              string `json:"id"`
				Entreprise      string `json:"entreprise"`
				MontantCentimes int64  `json:"montant_centimes"`
				Statut          string `json:"statut"`
			} `json:"devis"`
		} `json:"comparaisons"`
	}
	decodeStructured(t, callTool(t, session, "devis_liste", nil), &liste)

	if len(liste.Comparaisons) != 1 {
		t.Fatalf("comparaisons = %d, attendu 1", len(liste.Comparaisons))
	}
	comparaison := liste.Comparaisons[0]
	if comparaison.Demande.Lot != "Charpente" || comparaison.Demande.ID != demande.ID.String() {
		t.Errorf("demande = %+v, attendu le lot Charpente semé", comparaison.Demande)
	}
	if len(comparaison.Devis) != 1 || comparaison.Devis[0].MontantCentimes != int64(seeded.Montant) {
		t.Errorf("devis = %+v, attendu le devis semé à %d centimes", comparaison.Devis, seeded.Montant)
	}

	// Écriture : un second devis sur la même demande.
	var enregistre struct {
		ID     string `json:"id"`
		Statut string `json:"statut"`
	}
	decodeStructured(t, callTool(t, session, "devis_enregistrer", map[string]any{
		"demande_id":       demande.ID.String(),
		"entreprise":       "Toitures Réunies",
		"montant_centimes": 1_050_000,
		"recu_le":          "2026-05-12",
	}), &enregistre)

	if enregistre.Statut != "recu" {
		t.Errorf("statut = %q, attendu recu", enregistre.Statut)
	}

	stored, ok := tb.devis.devisParEntreprise("Toitures Réunies")
	if !ok {
		t.Fatal("le devis enregistré par MCP n'est pas dans le dépôt")
	}
	// La traçabilité : l'acteur du jeton signe l'écriture.
	if string(stored.RecordedBy) != testOwnerUserID {
		t.Errorf("RecordedBy = %q, attendu %q", stored.RecordedBy, testOwnerUserID)
	}
}

// TestDevisEnregistrerRefuseValiditeAbsurde vérifie la borne de validite_jours :
// une valeur qui ferait déborder la conversion en durée est refusée avec un
// message français, avant tout calcul.
func TestDevisEnregistrerRefuseValiditeAbsurde(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	demande, _ := tb.seedComparaison(t)

	session := tb.connect(t, tokenOwner)

	result := callTool(t, session, "devis_enregistrer", map[string]any{
		"demande_id":       demande.ID.String(),
		"entreprise":       "Toitures Réunies",
		"montant_centimes": 1_000_000,
		"recu_le":          "2026-05-12",
		"validite_jours":   999_999_999,
	})

	if !result.IsError {
		t.Fatal("une durée de validité absurde a été acceptée")
	}
	if text := resultText(result); !strings.Contains(text, "validité") {
		t.Errorf("le refus ne nomme pas la validité : %q", text)
	}
}

// TestEtapeDemarrer vérifie l'invariant du planning au travers du tool : pas de
// démarrage avant les prérequis, et le refus est le message français du
// domaine. Le tool relit lui-même l'étape pour la garde optimiste — un agent
// n'a pas de formulaire à état.
func TestEtapeDemarrer(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	prerequis := tb.seedEtape(t, "Gros œuvre")
	dependante := tb.seedEtape(t, "Charpente", prerequis.ID)

	session := tb.connect(t, tokenOwner)

	// Refus : le prérequis n'est pas terminé.
	blocked := callTool(t, session, "etape_demarrer", map[string]any{
		"etape_id": dependante.ID.String(),
	})
	if !blocked.IsError {
		t.Fatal("l'étape dépendante a démarré avant son prérequis")
	}
	if text := resultText(blocked); !strings.Contains(text, "prérequis") {
		t.Errorf("le refus n'explique pas les prérequis : %q", text)
	}

	// Le prérequis, lui, démarre.
	var started struct {
		Statut string `json:"statut"`
	}
	decodeStructured(t, callTool(t, session, "etape_demarrer", map[string]any{
		"etape_id": prerequis.ID.String(),
	}), &started)
	if started.Statut != planning.StatutEnCours.String() {
		t.Errorf("statut = %q, attendu en_cours", started.Statut)
	}

	// Et se termine.
	var finished struct {
		Statut string `json:"statut"`
	}
	decodeStructured(t, callTool(t, session, "etape_terminer", map[string]any{
		"etape_id": prerequis.ID.String(),
	}), &finished)
	if finished.Statut != planning.StatutTerminee.String() {
		t.Errorf("statut = %q, attendu terminee", finished.Statut)
	}
}

// TestAssurancePreparerEnvoi vérifie l'assemblage transverse et surtout ce que
// le tool NE fait PAS : le dossier porte l'avertissement en toutes lettres, et
// rien n'est envoyé nulle part — cet adapter n'a aucun port d'envoi.
func TestAssurancePreparerEnvoi(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	session := tb.connect(t, tokenOwner)

	// Une facture par le tool lui-même : hors devis, le cas le plus simple.
	facture := callTool(t, session, "facture_enregistrer", map[string]any{
		"entreprise":       "Matériaux du Centre",
		"montant_centimes": 25_000,
		"date":             "2026-06-01",
	})
	if facture.IsError {
		t.Fatalf("facture_enregistrer refusé : %s", resultText(facture))
	}

	var dossier struct {
		Avertissement string `json:"avertissement"`
		Factures      []struct {
			Facture struct {
				Entreprise string `json:"entreprise"`
			} `json:"facture"`
		} `json:"factures"`
		Totaux struct {
			FactureCentimes int64 `json:"facture_centimes"`
		} `json:"totaux"`
	}
	decodeStructured(t, callTool(t, session, "assurance_preparer_envoi", nil), &dossier)

	if !strings.Contains(dossier.Avertissement, "aucun envoi") {
		t.Errorf("avertissement = %q, il doit dire qu'aucun envoi n'est effectué", dossier.Avertissement)
	}
	if len(dossier.Factures) != 1 || dossier.Factures[0].Facture.Entreprise != "Matériaux du Centre" {
		t.Errorf("factures du dossier = %+v, attendu la facture enregistrée", dossier.Factures)
	}
	if dossier.Totaux.FactureCentimes != 25_000 {
		t.Errorf("total facturé = %d, attendu 25000", dossier.Totaux.FactureCentimes)
	}
}

// TestAcompteEnregistrer joue LA glu inter-domaines du tool d'écriture le plus
// délicat : la résolution du devis retenu et la transmission de son montant
// engagé EN VALEUR au domaine finance (R1/R2), puis l'invariant central —
// le cumul des acomptes ne dépasse pas l'engagé — au travers du protocole.
func TestAcompteEnregistrer(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	_, proposition := tb.seedComparaison(t) // 1 180 050 centimes
	if _, err := tb.devisService.Retain(t.Context(), proposition.ID, "seed"); err != nil {
		t.Fatalf("rétention du devis semé : %v", err)
	}

	session := tb.connect(t, tokenOwner)

	// Cas heureux : l'acompte est rattaché, sous l'engagé.
	var enregistre struct {
		ID              string `json:"id"`
		DevisID         string `json:"devis_id"`
		Entreprise      string `json:"entreprise"`
		MontantCentimes int64  `json:"montant_centimes"`
		Moyen           string `json:"moyen"`
		Assurance       struct {
			Statut string `json:"statut"`
		} `json:"assurance"`
	}
	decodeStructured(t, callTool(t, session, "acompte_enregistrer", map[string]any{
		"devis_id":         proposition.ID.String(),
		"entreprise":       "Charpentes du Val",
		"montant_centimes": 500_000,
		"date":             "2026-06-01",
		"moyen":            "virement",
	}), &enregistre)

	switch {
	case enregistre.DevisID != proposition.ID.String():
		t.Errorf("devis_id = %q, attendu le devis retenu %q", enregistre.DevisID, proposition.ID)
	case enregistre.MontantCentimes != 500_000:
		t.Errorf("montant_centimes = %d, attendu 500000", enregistre.MontantCentimes)
	case enregistre.Moyen != "virement":
		t.Errorf("moyen = %q, attendu virement", enregistre.Moyen)
	case enregistre.Assurance.Statut != "non_envoyee":
		t.Errorf("assurance = %q, un acompte naît non envoyé", enregistre.Assurance.Statut)
	}

	// La traçabilité : l'acteur du jeton signe l'écriture.
	acomptes, err := tb.financeService.Acomptes(t.Context())
	if err != nil {
		t.Fatalf("relecture des acomptes : %v", err)
	}
	if len(acomptes) != 1 || string(acomptes[0].RecordedBy) != testOwnerUserID {
		t.Fatalf("acomptes = %+v, attendu un seul, signé %q", acomptes, testOwnerUserID)
	}

	// Refus : un second acompte qui ferait déborder l'engagé (500 000 déjà
	// versés + 800 000 > 1 180 050). Le message explique l'invariant.
	overdraft := callTool(t, session, "acompte_enregistrer", map[string]any{
		"devis_id":         proposition.ID.String(),
		"entreprise":       "Charpentes du Val",
		"montant_centimes": 800_000,
		"date":             "2026-07-01",
		"moyen":            "virement",
	})
	if !overdraft.IsError {
		t.Fatal("un acompte au-dessus de l'engagé a été accepté")
	}
	if text := resultText(overdraft); !strings.Contains(text, "engagé") {
		t.Errorf("le refus n'explique pas le montant engagé : %q", text)
	}
}

// TestAcompteEnregistrerRefuseDevisNonRetenu : le pendant acompte du refus déjà
// testé pour les factures — un devis encore en comparaison ne s'engage pas.
func TestAcompteEnregistrerRefuseDevisNonRetenu(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	_, proposition := tb.seedComparaison(t) // statut « recu », pas retenu

	session := tb.connect(t, tokenOwner)

	result := callTool(t, session, "acompte_enregistrer", map[string]any{
		"devis_id":         proposition.ID.String(),
		"entreprise":       "Charpentes du Val",
		"montant_centimes": 100_000,
		"date":             "2026-06-01",
		"moyen":            "virement",
	})

	if !result.IsError {
		t.Fatal("un acompte a été rattaché à un devis non retenu")
	}
	if text := resultText(result); !strings.Contains(text, "retenu") {
		t.Errorf("le refus n'explique pas le statut du devis : %q", text)
	}
}

// TestFinancesSynthese vérifie la lecture transverse la plus assemblée : la
// ligne du devis retenu (engagé, payé, reste EN VALEURS calculées), la ligne
// hors devis et le total chantier.
func TestFinancesSynthese(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	_, proposition := tb.seedComparaison(t) // 1 180 050 centimes
	if _, err := tb.devisService.Retain(t.Context(), proposition.ID, "seed"); err != nil {
		t.Fatalf("rétention du devis semé : %v", err)
	}

	session := tb.connect(t, tokenOwner)

	// Un acompte sur le devis retenu et une facture hors devis, par les tools.
	if result := callTool(t, session, "acompte_enregistrer", map[string]any{
		"devis_id":         proposition.ID.String(),
		"entreprise":       "Charpentes du Val",
		"montant_centimes": 500_000,
		"date":             "2026-06-01",
		"moyen":            "virement",
	}); result.IsError {
		t.Fatalf("acompte_enregistrer refusé : %s", resultText(result))
	}
	if result := callTool(t, session, "facture_enregistrer", map[string]any{
		"entreprise":       "Location Bennes Service",
		"montant_centimes": 25_000,
		"date":             "2026-06-05",
	}); result.IsError {
		t.Fatalf("facture_enregistrer refusé : %s", resultText(result))
	}

	var synthese struct {
		Lignes []struct {
			DevisID             string `json:"devis_id"`
			EngageCentimes      int64  `json:"engage_centimes"`
			PayeCentimes        int64  `json:"paye_centimes"`
			ResteAPayerCentimes int64  `json:"reste_a_payer_centimes"`
		} `json:"lignes"`
		HorsDevis *struct {
			FactureCentimes int64 `json:"facture_centimes"`
		} `json:"hors_devis"`
		Total struct {
			FactureCentimes int64 `json:"facture_centimes"`
			PayeCentimes    int64 `json:"paye_centimes"`
		} `json:"total"`
	}
	decodeStructured(t, callTool(t, session, "finances_synthese", nil), &synthese)

	if len(synthese.Lignes) != 1 {
		t.Fatalf("lignes = %d, attendu la seule ligne du devis retenu", len(synthese.Lignes))
	}
	ligne := synthese.Lignes[0]
	switch {
	case ligne.DevisID != proposition.ID.String():
		t.Errorf("devis_id = %q, attendu %q", ligne.DevisID, proposition.ID)
	case ligne.EngageCentimes != 1_180_050:
		t.Errorf("engage_centimes = %d, attendu 1180050", ligne.EngageCentimes)
	case ligne.PayeCentimes != 500_000:
		t.Errorf("paye_centimes = %d, attendu 500000", ligne.PayeCentimes)
	case ligne.ResteAPayerCentimes != 680_050:
		t.Errorf("reste_a_payer_centimes = %d, attendu 680050", ligne.ResteAPayerCentimes)
	}
	if synthese.HorsDevis == nil || synthese.HorsDevis.FactureCentimes != 25_000 {
		t.Errorf("hors_devis = %+v, attendu 25000 centimes facturés", synthese.HorsDevis)
	}
	if synthese.Total.FactureCentimes != 25_000 || synthese.Total.PayeCentimes != 500_000 {
		t.Errorf("total = %+v, attendu 25000 facturés et 500000 payés", synthese.Total)
	}
}

// TestFinancesFacturesEtAcomptes : les deux listes brutes rendent ce que le
// domaine a stocké, dans le vocabulaire français des tools.
func TestFinancesFacturesEtAcomptes(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	session := tb.connect(t, tokenOwner)

	if result := callTool(t, session, "facture_enregistrer", map[string]any{
		"entreprise":       "Matériaux du Centre",
		"montant_centimes": 25_000,
		"date":             "2026-06-01",
		"numero":           "F-2026-042",
	}); result.IsError {
		t.Fatalf("facture_enregistrer refusé : %s", resultText(result))
	}
	if result := callTool(t, session, "acompte_enregistrer", map[string]any{
		"entreprise":       "Terrassement Léger",
		"montant_centimes": 40_000,
		"date":             "2026-06-02",
		"moyen":            "cheque",
	}); result.IsError {
		t.Fatalf("acompte_enregistrer refusé : %s", resultText(result))
	}

	var factures struct {
		Factures []struct {
			Entreprise      string `json:"entreprise"`
			Numero          string `json:"numero"`
			MontantCentimes int64  `json:"montant_centimes"`
			Paiement        string `json:"paiement"`
			Assurance       struct {
				Statut string `json:"statut"`
			} `json:"assurance"`
		} `json:"factures"`
	}
	decodeStructured(t, callTool(t, session, "finances_factures", nil), &factures)
	if len(factures.Factures) != 1 {
		t.Fatalf("factures = %d, attendu 1", len(factures.Factures))
	}
	facture := factures.Factures[0]
	if facture.Entreprise != "Matériaux du Centre" || facture.Numero != "F-2026-042" ||
		facture.MontantCentimes != 25_000 || facture.Paiement != "impayee" ||
		facture.Assurance.Statut != "non_envoyee" {
		t.Errorf("facture = %+v, ne rend pas la pièce stockée", facture)
	}

	var acomptes struct {
		Acomptes []struct {
			Entreprise      string `json:"entreprise"`
			MontantCentimes int64  `json:"montant_centimes"`
			Moyen           string `json:"moyen"`
		} `json:"acomptes"`
	}
	decodeStructured(t, callTool(t, session, "finances_acomptes", nil), &acomptes)
	if len(acomptes.Acomptes) != 1 {
		t.Fatalf("acomptes = %d, attendu 1", len(acomptes.Acomptes))
	}
	acompte := acomptes.Acomptes[0]
	if acompte.Entreprise != "Terrassement Léger" || acompte.MontantCentimes != 40_000 || acompte.Moyen != "cheque" {
		t.Errorf("acompte = %+v, ne rend pas la pièce stockée", acompte)
	}
}

// TestPlanningEtapesEtJalons : les lectures du planning rendent statuts
// DÉRIVÉS et dépendances, et les jalons leur état d'atteinte.
func TestPlanningEtapesEtJalons(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	prerequis := tb.seedEtape(t, "Gros œuvre")
	dependante := tb.seedEtape(t, "Charpente", prerequis.ID)

	if _, err := tb.planningService.CreateJalon(t.Context(), planning.JalonInput{
		Name: "Hors d'eau",
		Date: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
		By:   "seed",
	}); err != nil {
		t.Fatalf("création du jalon : %v", err)
	}

	session := tb.connect(t, tokenOwner)

	var etapes struct {
		Etapes []struct {
			ID       string   `json:"id"`
			Nom      string   `json:"nom"`
			Statut   string   `json:"statut"`
			DependDe []string `json:"depend_de"`
		} `json:"etapes"`
	}
	decodeStructured(t, callTool(t, session, "planning_etapes", nil), &etapes)
	if len(etapes.Etapes) != 2 {
		t.Fatalf("étapes = %d, attendu 2", len(etapes.Etapes))
	}
	byName := map[string]int{}
	for i, etape := range etapes.Etapes {
		byName[etape.Nom] = i
		if etape.Statut != planning.StatutPrevue.String() {
			t.Errorf("étape %s : statut = %q, une étape semée naît prévue", etape.Nom, etape.Statut)
		}
	}
	charpente := etapes.Etapes[byName["Charpente"]]
	if len(charpente.DependDe) != 1 || charpente.DependDe[0] != dependante.DependsOn[0].String() {
		t.Errorf("depend_de = %v, attendu le prérequis %q", charpente.DependDe, prerequis.ID)
	}

	var jalons struct {
		Jalons []struct {
			Nom     string `json:"nom"`
			Date    string `json:"date"`
			Atteint bool   `json:"atteint"`
		} `json:"jalons"`
	}
	decodeStructured(t, callTool(t, session, "planning_jalons", nil), &jalons)
	if len(jalons.Jalons) != 1 {
		t.Fatalf("jalons = %d, attendu 1", len(jalons.Jalons))
	}
	jalon := jalons.Jalons[0]
	if jalon.Nom != "Hors d'eau" || jalon.Date != "2026-09-30" || jalon.Atteint {
		t.Errorf("jalon = %+v, attendu Hors d'eau au 2026-09-30, non atteint", jalon)
	}
}

// TestDevisDetail : la comparaison d'une demande précise, et le refus lisible
// d'une demande inconnue.
func TestDevisDetail(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	demande, seeded := tb.seedComparaison(t)

	session := tb.connect(t, tokenOwner)

	var comparaison struct {
		Demande struct {
			Lot string `json:"lot"`
		} `json:"demande"`
		Devis []struct {
			ID              string `json:"id"`
			MontantCentimes int64  `json:"montant_centimes"`
		} `json:"devis"`
		Close bool `json:"close"`
	}
	decodeStructured(t, callTool(t, session, "devis_detail", map[string]any{
		"demande_id": demande.ID.String(),
	}), &comparaison)

	if comparaison.Demande.Lot != "Charpente" || comparaison.Close {
		t.Errorf("comparaison = %+v, attendu le lot Charpente encore ouvert", comparaison)
	}
	if len(comparaison.Devis) != 1 || comparaison.Devis[0].ID != seeded.ID.String() ||
		comparaison.Devis[0].MontantCentimes != int64(seeded.Montant) {
		t.Errorf("devis = %+v, attendu le devis semé", comparaison.Devis)
	}

	inconnue := callTool(t, session, "devis_detail", map[string]any{
		"demande_id": "6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5",
	})
	if !inconnue.IsError {
		t.Fatal("une demande inconnue a rendu une comparaison")
	}
}

// TestDocumentsListe : les métadonnées des pièces — jamais leur contenu — avec
// le rattachement en références faibles.
func TestDocumentsListe(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	_, proposition := tb.seedComparaison(t)

	content := []byte("%PDF-1.4 contenu de demonstration")
	if _, err := tb.documentService.Upload(t.Context(), document.UploadInput{
		FileName:  "devis-signe.pdf",
		MimeType:  "application/pdf",
		SizeBytes: int64(len(content)),
		Content:   bytes.NewReader(content),
		Category:  "devis_signe",
		Target:    document.Target{Type: document.TargetDevis, ID: proposition.ID.String()},
		By:        "seed",
	}); err != nil {
		t.Fatalf("dépôt de la pièce : %v", err)
	}

	session := tb.connect(t, tokenOwner)

	var liste struct {
		Documents []struct {
			NomFichier   string `json:"nom_fichier"`
			TypeMime     string `json:"type_mime"`
			TailleOctets int64  `json:"taille_octets"`
			Categorie    string `json:"categorie"`
			CibleType    string `json:"cible_type"`
			CibleID      string `json:"cible_id"`
		} `json:"documents"`
	}
	decodeStructured(t, callTool(t, session, "documents_liste", nil), &liste)

	if len(liste.Documents) != 1 {
		t.Fatalf("documents = %d, attendu 1", len(liste.Documents))
	}
	doc := liste.Documents[0]
	if doc.NomFichier != "devis-signe.pdf" || doc.TypeMime != "application/pdf" ||
		doc.TailleOctets != int64(len(content)) || doc.Categorie != "devis_signe" ||
		doc.CibleType != "devis" || doc.CibleID != proposition.ID.String() {
		t.Errorf("document = %+v, ne rend pas les métadonnées déposées", doc)
	}
}

// TestFactureEnregistrerRefuseDevisNonRetenu vérifie la résolution transverse :
// rattacher une facture à un devis encore en comparaison est refusé avec un
// message explicable, comme sur le web.
func TestFactureEnregistrerRefuseDevisNonRetenu(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	_, proposition := tb.seedComparaison(t) // statut « recu », pas retenu

	session := tb.connect(t, tokenOwner)

	result := callTool(t, session, "facture_enregistrer", map[string]any{
		"devis_id":         proposition.ID.String(),
		"entreprise":       "Charpentes du Val",
		"montant_centimes": 100_000,
		"date":             "2026-06-01",
	})

	if !result.IsError {
		t.Fatal("une facture a été rattachée à un devis non retenu")
	}
	if text := resultText(result); !strings.Contains(text, "retenu") {
		t.Errorf("le refus n'explique pas le statut du devis : %q", text)
	}
}
